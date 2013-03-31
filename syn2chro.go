package main

import (
	"bufio"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	CONFIG_JSON_PATH_DEF = "syn2chro.json"
	FILE_DB_PREFIX       = "syn2chro"
	FILE_DB_NUMBER_NAME  = "syn2chro_number.json"
)

type User interface {
	Auth(name, pass string) bool
	GetPath(name, pass string) (string, error)
	GetId(name, pass string) (int, error)
}

type Merge interface {
	Merge(src1, src2 []byte) ([]byte, error)
}

type DB interface {
	GetSyncNumber(string) int
	SetSyncNumber(string, int) error
	GetData(string) ([]byte, error)
	SetData(string, []byte) error
}

type Config struct {
	Name            string  `json:"ユーザー名"`
	Pass            string  `json:"パスワード"`
	Addr            string  `json:"アドレス"`
	Port            float64 `json:"ポート番号"`
	ReadTimeoutSec  float64 `json:"読み込みタイムアウト秒"`
	WriteTimeoutSec float64 `json:"書き込みタイムアウト秒"`
	LogFilePath     string  `json:"ログファイルパス"`
	ApiURL          string  `json:"APIのURL"`
	DBPath          string  `json:"データベースファイルのルートパス"`
	DebugPrint		bool	`json:"デバッグ出力"`
}

// 1ユーザー専用認証機
type OneUser struct {
	name string
	pass string
}

// お手軽ファイルDB
type FileDB struct {
	root string
}

type FileDBNumber struct {
	SyncNum float64 `json:"sync_number"`
}

type SyncHandle struct {
	conf Config
}

type Session struct {
	w    http.ResponseWriter
	wbuf *bufio.Writer
	r    *http.Request
	user User
	db   DB
}

type Thread struct {
	Status string `xml:"s,attr,omitempty"`
	Url    string `xml:"url,attr"`
	Title  string `xml:"title,attr,omitempty"`
	Read   int    `xml:"read,attr,omitempty"`
	Now    int    `xml:"now,attr,omitempty"`
	Count  int    `xml:"count,attr,omitempty"`
}

type Board struct {
	Url   string `xml:"url,attr"`
	Title string `xml:"title,attr"`
}

type Dir struct {
	Name       string   `xml:"name,attr"`
	ThreadList []Thread `xml:"thread"`
	BoardList  []Board  `xml:"board"`
	DirList    []Dir    `xml:"dir"` // 入れ子にできる
}

type ThreadGroup struct {
	Cate       string   `xml:"category,attr"`
	ThreadList []Thread `xml:"thread"`
	BoardList  []Board  `xml:"board"`
	DirList    []Dir    `xml:"dir"`
}

type GroupMap struct {
	tm map[string]Thread
	bm map[string]Board
	dm map[string]GroupMap
}

type Request struct {
	XMLName    xml.Name            `xml:"sync2ch_request"`
	SyncNum    int                 `xml:"sync_number,attr"`
	ClientVer  string              `xml:"client_version,attr"`
	ClientName string              `xml:"client_name,attr"`
	Os         string              `xml:"os,attr"`
	Tg         []ThreadGroup       `xml:"thread_group"`
	TgMap      map[string]GroupMap `xml:"-"`
}

type Response struct {
	XMLName xml.Name            `xml:"sync2ch_response"`
	SyncNum int                 `xml:"sync_number,attr"`
	Tg      []ThreadGroup       `xml:"thread_group"`
	TgMap   map[string]GroupMap `xml:"-"`
}

type Sync2ch_v1 struct {
	load Request
	req  Request
	save Request
	res  Response
}

var g_log *log.Logger
var g_debug bool

func main() {
	c := readConfig()
	var w io.Writer
	if c.LogFilePath == "" {
		w = os.Stdout
	} else {
		fp, err := os.OpenFile(c.LogFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0666)
		if err != nil {
			log.Fatal("file open error")
		}
		//defer fp.Close()	実行終了で開放
		w = fp
	}
	g_log = log.New(w, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	g_debug = c.DebugPrint

	myHandler := &SyncHandle{
		conf: c,
	}
	server := &http.Server{
		Addr:           fmt.Sprintf("%s:%d", c.Addr, int(c.Port)),
		Handler:        myHandler,
		ReadTimeout:    time.Duration(c.ReadTimeoutSec) * time.Second,
		WriteTimeout:   time.Duration(c.WriteTimeoutSec) * time.Second,
		MaxHeaderBytes: 1024 * 1024,
	}
	g_log.Printf("listen start %s:%d\n", c.Addr, int(c.Port))
	// サーバ起動
	g_log.Fatal(server.ListenAndServe())
}

func (h *SyncHandle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ses := &Session{
		w:    w,
		wbuf: bufio.NewWriterSize(w, 1024*1024),
		r:    r,
		user: &OneUser{
			name: h.conf.Name,
			pass: h.conf.Pass,
		},
		db: &FileDB{
			root: h.conf.DBPath,
		},
	}

	if r.Method != "POST" {
		ses.statusCode(http.StatusNotImplemented) // 501 POSTしか実装していない
		return
	}
	if r.URL.Path != h.conf.ApiURL {
		ses.statusCode(http.StatusNotFound) // 404 そんなもんはねーよ
		return
	}

	// 認証
	name, pass, err := ses.auth()
	if err != nil {
		ses.statusCode(http.StatusUnauthorized) // 401 認証失敗
		return
	}

	// リクエスト取得
	reqdata, err := ses.getRequestData()
	if err != nil {
		ses.statusCode(http.StatusBadRequest) // 400 お前のリクエストが悪い
		return
	}
	debugPrint(string(reqdata))

	// ユーザーごとのパスを取得
	path, _ := ses.user.GetPath(name, pass)
	// DBから最新の同期情報を取得
	motodata, err := ses.db.GetData(path)
	if err != nil {
		if ses.db.GetSyncNumber(path) < 0 {
			motodata = nil
		} else {
			// 初期同期じゃないのにデータの読み込みに失敗
			ses.statusCode(http.StatusInternalServerError) // 500 サーバーさん調子悪い
			debugPrint(err.Error())
			return
		}
	}

	// 解析
	sync := &Sync2ch_v1{}
	d1, d2, err := sync.Merge(motodata, reqdata)
	if err != nil {
		// 解析に失敗
		ses.statusCode(http.StatusInternalServerError) // 500 サーバーさん調子悪い
		debugPrint(err.Error())
		return
	}

	// 同期番号の保存
	err = ses.db.SetSyncNumber(path, sync.save.SyncNum)
	if err != nil {
		// データの保存に失敗
		ses.statusCode(http.StatusInternalServerError) // 500 サーバーさん調子悪い
		debugPrint(err.Error())
		return
	}
	// データの保存
	err = ses.db.SetData(path, d1)
	if err != nil {
		// データの保存に失敗
		ses.statusCode(http.StatusInternalServerError) // 500 サーバーさん調子悪い
		debugPrint(err.Error())
		return
	}

	// クライアントへ送信
	err = ses.send(d2)
	if err != nil {
		ses.statusCode(http.StatusInternalServerError) // 500 サーバーさん調子悪い
		debugPrint(err.Error())
		return
	}
	debugPrint(string(d2))
	ses.statusCode(http.StatusOK)
	// データの送信
	ses.wbuf.Flush()
}

func (ses *Session) statusCode(code int) {
	// ステータスコード出力
	ses.w.WriteHeader(code)
	g_log.Printf("%s - \"%s %s %s\" %d %d", ses.r.RemoteAddr, ses.r.Method, ses.r.RequestURI, ses.r.Proto, code, ses.r.ContentLength)
}

func (ses *Session) auth() (name, pass string, err error) {
	// 認証
	auth := ses.r.Header.Get("Authorization")
	if len(auth) > 6 && auth[:6] == "Basic " {
		deco, err64 := base64.StdEncoding.DecodeString(auth[6:])
		if err64 == nil {
			list := strings.SplitN(string(deco), ":", 2)
			if ses.user.Auth(list[0], list[1]) {
				name = list[0]
				pass = list[1]
				err = nil
			} else {
				err = errors.New("Authorization error")
			}
		} else {
			// Base64のデコードに失敗
			err = err64
		}
	} else {
		err = errors.New("Authorization error")
	}
	return
}

func (ses *Session) getRequestData() (data []byte, readerr error) {
	if ses.r.Header.Get("Encoding") == "gzip" {
		// データが圧縮されているかも
		zip, err := gzip.NewReader(ses.r.Body)
		if err == nil {
			defer zip.Close()
			data, readerr = ioutil.ReadAll(zip)
		} else {
			readerr = errors.New("unzip error")
		}
	} else {
		// 普通に読み込む
		data, readerr = ioutil.ReadAll(ses.r.Body)
	}
	return
}

func (ses *Session) send(data []byte) (reterr error) {
	if ses.r.Header.Get("Accespt-Encoding") == "gzip" {
		// 圧縮して送る
		wfp, err := gzip.NewWriterLevel(ses.wbuf, gzip.BestSpeed)
		if err == nil {
			defer wfp.Close()
			ses.w.Header().Set("Content-Encoding", "gzip")
			wfp.Write([]byte(xml.Header))
			wfp.Write(data)
		} else {
			reterr = err
		}
	} else {
		// 普通に送る
		ses.wbuf.Write([]byte(xml.Header))
		ses.wbuf.Write(data)
	}
	return
}

func (v1 *Sync2ch_v1) Merge(src1, src2 []byte) (d1, d2 []byte, reterr error) {
	if src2 == nil {
		reterr = errors.New("nil")
		return
	}
	// バイト列から変換
	reterr = xml.Unmarshal(src2, &v1.req)
	if reterr != nil {
		return
	}
	if src1 != nil {
		reterr = xml.Unmarshal(src1, &v1.load)
		if reterr != nil {
			return
		}
	} else {
		v1.load = v1.req
	}
	// 全体更新
	v1.update()

	// バイト列に変換
	d1, reterr = xml.MarshalIndent(&v1.save, "", "\t")
	if reterr != nil {
		d1 = nil
		d2 = nil
		return
	}
	d2, reterr = xml.MarshalIndent(&v1.res, "", "\t")
	if reterr != nil {
		d1 = nil
		d2 = nil
		return
	}
	return
}

func (v1 *Sync2ch_v1) update() {
	v1.save = v1.load
	if v1.load.SyncNum == v1.req.SyncNum {
		// 情報の更新
		v1.createUpdateRes()
		v1.mergeReq()
	} else if v1.load.SyncNum > v1.req.SyncNum {
		// 同期
		v1.createSyncRes()
	} else {
		// サーバの方が小さい番号になることはありえないはず…
		// とりあえず大きい数字に更新
		v1.load.SyncNum = v1.req.SyncNum
		v1.save.SyncNum = v1.req.SyncNum
		v1.createUpdateRes()
		v1.mergeReq()
	}
}

func (v1 *Sync2ch_v1) mergeReq() {
	m := make(map[string]int)
	if v1.save.Tg == nil {
		v1.save.Tg = []ThreadGroup{}
	}
	for i, it := range v1.save.Tg {
		m[it.Cate] = i
	}
	for _, it := range v1.req.Tg {
		if index, ok := m[it.Cate]; ok {
			v1.save.Tg[index] = it
		} else {
			// 後ろに伸びるだけだからインデックスは狂わないはず
			v1.save.Tg = append(v1.save.Tg, it)
		}
	}
	v1.save.ClientVer = v1.req.ClientVer
	v1.save.ClientName = v1.req.ClientName
	v1.save.Os = v1.req.Os
}

// リクエストのSyncNumberが同じ場合
// 全部none
func (v1 *Sync2ch_v1) createUpdateRes() {
	add := []ThreadGroup{}
	v1.req.TgMap = convertMap(v1.req.Tg)
	for key, re := range v1.req.TgMap {
		add = append(add, createUpdateResThreadGroup(re, key))
	}
	v1.res.Tg = add
	v1.updateSyncNumber()
}

// リクエストのSyncNumberが古い場合
// 同期する
func (v1 *Sync2ch_v1) createSyncRes() {
	add := []ThreadGroup{}
	v1.load.TgMap = convertMap(v1.load.Tg)
	v1.req.TgMap = convertMap(v1.req.Tg)
	for key, re := range v1.req.TgMap {
		if it, ok := v1.load.TgMap[key]; ok {
			add = append(add, createSyncResThreadGroup(it, re, key))
		} else {
			// 最新版には無いカテゴリーのもよう
		}
	}
	v1.res.Tg = add
	v1.updateSyncNumber()
}

func (v1 *Sync2ch_v1) updateSyncNumber() {
	// 番号の更新
	v1.save.SyncNum++
	v1.res.SyncNum = v1.save.SyncNum
}

// ちゃんとしたDBとかに後々対応できるように…

func (db *FileDB) GetSyncNumber(name string) int {
	p := db.root + "/" + name + "/" + FILE_DB_NUMBER_NAME
	filedata, err := ioutil.ReadFile(p)
	if err != nil {
		return -1
	}
	dbn := FileDBNumber{}
	err = json.Unmarshal(filedata, &dbn)
	if err != nil {
		return -1
	}
	return int(dbn.SyncNum)
}

func (db *FileDB) SetSyncNumber(name string, num int) (reterr error) {
	p := db.root + "/" + name + "/" + FILE_DB_NUMBER_NAME
	dbn := FileDBNumber{
		SyncNum: float64(num),
	}
	var data []byte
	data, reterr = json.MarshalIndent(&dbn, "", "\t")
	if reterr != nil {
		return
	}
	reterr = db.checkPath(p)
	if reterr != nil {
		return
	}
	reterr = ioutil.WriteFile(p, data, 0744)
	if reterr != nil {
		return
	}
	return nil
}

func (db *FileDB) GetData(name string) ([]byte, error) {
	var data []byte
	p, err := db.createPath(name)
	if err == nil {
		data, err = ioutil.ReadFile(p)
	}
	return data, err
}

func (db *FileDB) SetData(name string, data []byte) error {
	p, err := db.createPath(name)
	if err == nil {
		err = ioutil.WriteFile(p, data, 0744)
	}
	return err
}

func (db *FileDB) createPath(name string) (string, error) {
	num := db.GetSyncNumber(name)
	if num < 0 {
		return "", errors.New("sync number error")
	}
	p := fmt.Sprintf("%s/%s/%s_%d.xml", db.root, name, FILE_DB_PREFIX, num)
	return p, db.checkPath(p)
}

func (db *FileDB) checkPath(path string) (reterr error) {
	dir := filepath.Dir(path)
	_, reterr = os.Stat(dir)
	if reterr != nil {
		reterr = os.MkdirAll(dir, 0744)
	}
	return
}

// マルチユーザーに後々対応できるように…

func (ou *OneUser) Auth(name, pass string) bool {
	return name == ou.name && pass == ou.pass
}

func (ou *OneUser) GetPath(name, pass string) (string, error) {
	if ou.Auth(name, pass) == false {
		return "", errors.New("auth error")
	}
	return name, nil
}

func (ou *OneUser) GetId(name, pass string) (int, error) {
	if ou.Auth(name, pass) == false {
		return 0, errors.New("auth error")
	}
	return 1, nil // ユーザーは一人しか居ないので
}

func convertMap(tglist []ThreadGroup) map[string]GroupMap {
	tgmap := make(map[string]GroupMap)
	if tglist == nil {
		return tgmap
	}
	for _, it := range tglist {
		tgmap[it.Cate] = convertMapTg(it)
	}
	return tgmap
}

func convertMapTg(tg ThreadGroup) GroupMap {
	gm := GroupMap{
		tm: make(map[string]Thread),
		bm: make(map[string]Board),
		dm: make(map[string]GroupMap),
	}
	if tg.ThreadList != nil {
		for _, it := range tg.ThreadList {
			gm.tm[it.Url] = it
		}
	}
	if tg.BoardList != nil {
		for _, it := range tg.BoardList {
			gm.bm[it.Url] = it
		}
	}
	if tg.DirList != nil {
		for _, it := range tg.DirList {
			gm.dm[it.Name] = convertMapTg(ThreadGroup{
				ThreadList: it.ThreadList,
				BoardList:  it.BoardList,
				DirList:    it.DirList,
			})
		}
	}
	return gm
}

func createUpdateResDir(req GroupMap, name string) Dir {
	data := Dir{
		Name: name,
	}
	t := []Thread{}
	b := []Board{}
	d := []Dir{}

	for _, it := range req.tm {
		t = append(t, Thread{
			Url:    it.Url,
			Status: "n",
		})
	}
	for _, it := range req.bm {
		b = append(b, it)
	}
	for name, it := range req.dm {
		d = append(d, createUpdateResDir(it, name))
	}

	if len(t) > 0 {
		data.ThreadList = t
	}
	if len(b) > 0 {
		data.BoardList = b
	}
	if len(d) > 0 {
		data.DirList = d
	}
	return data
}

func createUpdateResThreadGroup(req GroupMap, key string) ThreadGroup {
	data := ThreadGroup{
		Cate: key,
	}
	t := []Thread{}
	b := []Board{}
	d := []Dir{}

	for _, it := range req.tm {
		t = append(t, Thread{
			Url:    it.Url,
			Status: "n",
		})
	}
	for _, it := range req.bm {
		b = append(b, it)
	}
	for name, it := range req.dm {
		d = append(d, createUpdateResDir(it, name))
	}

	if len(t) > 0 {
		data.ThreadList = t
	}
	if len(b) > 0 {
		data.BoardList = b
	}
	if len(d) > 0 {
		data.DirList = d
	}
	return data
}

func createSyncResThread(load, req *Thread) Thread {
	ret := Thread{
		Url: (*load).Url,
	}
	if req == nil {
		ret.Status = "a"
		ret.Title = (*load).Title
		ret.Read = (*load).Read
		ret.Now = (*load).Now
	} else if req.Read != (*load).Read || req.Now != (*load).Now || req.Count != (*load).Count {
		ret.Status = "u"
		ret.Read = (*load).Read
		ret.Now = (*load).Now
	} else {
		ret.Status = "n"
	}
	return ret
}

func createSyncResDir(load, req *GroupMap, name string) Dir {
	data := Dir{
		Name: name,
	}
	t := []Thread{}
	b := []Board{}
	d := []Dir{}

	if req == nil {
		// リクエストには無いフォルダ
		for _, it := range (*load).tm {
			t = append(t, createSyncResThread(&it, nil))
		}
		for _, it := range (*load).bm {
			b = append(b, it)
		}
		for name, it := range (*load).dm {
			d = append(d, createSyncResDir(&it, nil, name))
		}
	} else {
		for url, it := range (*load).tm {
			if re, ok := (*req).tm[url]; ok {
				t = append(t, createSyncResThread(&it, &re))
			} else {
				t = append(t, createSyncResThread(&it, nil))
			}
		}
		for _, it := range (*load).bm {
			b = append(b, it)
		}
		for name, it := range (*load).dm {
			if re, ok := (*req).dm[name]; ok {
				d = append(d, createSyncResDir(&it, &re, name))
			} else {
				d = append(d, createSyncResDir(&it, nil, name))
			}
		}
	}

	if len(t) > 0 {
		data.ThreadList = t
	}
	if len(b) > 0 {
		data.BoardList = b
	}
	if len(d) > 0 {
		data.DirList = d
	}
	return data
}

func createSyncResThreadGroup(load, req GroupMap, key string) ThreadGroup {
	data := ThreadGroup{
		Cate: key,
	}
	t := []Thread{}
	b := []Board{}
	d := []Dir{}

	for url, it := range load.tm {
		if re, ok := req.tm[url]; ok {
			t = append(t, createSyncResThread(&it, &re))
		} else {
			t = append(t, createSyncResThread(&it, nil))
		}
	}
	for _, it := range load.bm {
		b = append(b, it)
	}
	for name, it := range load.dm {
		if re, ok := req.dm[name]; ok {
			d = append(d, createSyncResDir(&it, &re, name))
		} else {
			d = append(d, createSyncResDir(&it, nil, name))
		}
	}

	if len(t) > 0 {
		data.ThreadList = t
	}
	if len(b) > 0 {
		data.BoardList = b
	}
	if len(d) > 0 {
		data.DirList = d
	}
	return data
}

func readConfig() Config {
	c := Config{}
	argc := len(os.Args)
	var path string
	if argc == 2 {
		path = os.Args[1]
	} else {
		path = CONFIG_JSON_PATH_DEF
	}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stdout, "conf read error")
		os.Exit(1)
	}
	err = json.Unmarshal(data, &c)
	if err != nil {
		fmt.Fprintf(os.Stdout, "conf json error")
		os.Exit(1)
	}
	return c
}

func debugPrint(str string) {
	if g_debug {
		g_log.Print(str)
	}
}

