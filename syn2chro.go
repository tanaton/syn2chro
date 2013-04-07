package main

import (
	"bytes"
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
	FILE_ITEM_DB_NAME    = "syn2chro_item.json"
)

type User interface {
	Auth(string, string) bool
	GetPath(string, string) (string, error)
	GetId(string, string) (int, error)
}

type Merge interface {
	Merge([]byte, []byte, []byte) ([]byte, error)
}

type DB interface {
	GetSyncNumber(string) int
	SetSyncNumber(string, int) error
	GetData(string) ([]byte, error)
	SetData(string, []byte) error
	GetItemData(string) ([]byte, error)
	SetItemData(string, []byte) error
}

type Config struct {
	Name            string  `json:"ユーザー名"`
	Pass            string  `json:"パスワード"`
	Addr            string  `json:"アドレス"`
	Port            float64 `json:"ポート番号"`
	ReadTimeoutSec  float64 `json:"読み込みタイムアウト秒"`
	WriteTimeoutSec float64 `json:"書き込みタイムアウト秒"`
	LogFilePath     string  `json:"ログファイルパス"`
	DBPath          string  `json:"データベースファイルのルートパス"`
	DebugPrint      bool    `json:"デバッグ出力"`
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
	wbuf *bytes.Buffer
	w    http.ResponseWriter
	r    *http.Request
	user User
	db   DB
}

/////////////////////////////////////////////////////////////////////
// Sync2ch Version1
/////////////////////////////////////////////////////////////////////
type ItemMap_v1 struct {
	ThreadMap map[string]int `json:"ThreadMap,omitempty"`
	BoardMap  map[string]int `json:"BoardMap,omitempty"`
	DirMap    map[string]int `json:"DirMap,omitempty"`
	GroupMap  map[string]int `json:"GroupMap,omitempty"`
	SyncNum   int            `json:"-"`
}

type Thread_v1 struct {
	Status string `xml:"s,attr,omitempty"`
	Url    string `xml:"url,attr"`
	Title  string `xml:"title,attr,omitempty"`
	Read   int    `xml:"read,attr,omitempty"`
	Now    int    `xml:"now,attr,omitempty"`
	Count  int    `xml:"count,attr,omitempty"`
}

type Board_v1 struct {
	Url   string `xml:"url,attr"`
	Title string `xml:"title,attr"`
}

type Dir_v1 struct {
	Name       string      `xml:"name,attr"`
	ThreadList []Thread_v1 `xml:"thread"`
	BoardList  []Board_v1  `xml:"board"`
	DirList    []Dir_v1    `xml:"dir"` // 入れ子にできる
}

type ThreadGroup_v1 struct {
	Cate       string      `xml:"category,attr"`
	ThreadList []Thread_v1 `xml:"thread"`
	BoardList  []Board_v1  `xml:"board"`
	DirList    []Dir_v1    `xml:"dir"`
}

type GroupMap_v1 struct {
	tm map[string]Thread_v1
	bm map[string]Board_v1
	dm map[string]GroupMap_v1
}

type Request_v1 struct {
	XMLName    xml.Name               `xml:"sync2ch_request"`
	SyncNum    int                    `xml:"sync_number,attr"`
	ClientVer  string                 `xml:"client_version,attr"`
	ClientName string                 `xml:"client_name,attr"`
	Os         string                 `xml:"os,attr"`
	Tg         []ThreadGroup_v1       `xml:"thread_group"`
	TgMap      map[string]GroupMap_v1 `xml:"-"`
}

type Response_v1 struct {
	XMLName xml.Name               `xml:"sync2ch_response"`
	Result  string                 `xml:"result,attr"`
	SyncNum int                    `xml:"sync_number,attr"`
	Tg      []ThreadGroup_v1       `xml:"thread_group"`
	TgMap   map[string]GroupMap_v1 `xml:"-"`
}

type Sync2ch_v1 struct {
	load Request_v1
	req  Request_v1
	save Request_v1
	res  Response_v1
	item ItemMap_v1
}

var g_log *log.Logger
var g_debug bool

func main() {
	c := readConfig()
	var w io.Writer
	if c.LogFilePath == "" {
		w = os.Stdout
	} else {
		fp, err := os.OpenFile(c.LogFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0744)
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
		wbuf: &bytes.Buffer{},
		w:    w,
		r:    r,
		user: &OneUser{
			name: h.conf.Name,
			pass: h.conf.Pass,
		},
		db: &FileDB{
			root: h.conf.DBPath,
		},
	}

	// Version1
	code, err := ses.execVer1()
	ses.statusCode(code)
	if err != nil {
		debugPrint(err.Error())
	}
	// データの送信
	io.Copy(ses.w, ses.wbuf)
}

func (ses *Session) execVer1() (code int, reterr error) {
	// 認証
	name, pass, err := ses.auth()
	if err == nil {
		// 認証成功
		switch ses.r.URL.Path {
		case "/api/sync1":
			// 同期する
			if ses.r.Method == "POST" {
				code, reterr = ses.execVer1Sync(name, pass)
			} else {
				code = http.StatusNotImplemented // 501
			}
		default:
			code = http.StatusNotFound // 404
		}
	} else {
		// 認証失敗
		code, reterr = http.StatusUnauthorized, err // 401
	}
	return
}

func (ses *Session) execVer1Sync(name, pass string) (int, error) {
	// リクエスト取得
	reqdata, err := ses.getRequestData()
	if err != nil {
		return http.StatusBadRequest, err // 400
	}
	debugPrint(string(reqdata))

	// ユーザーごとのパスを取得
	path, _ := ses.user.GetPath(name, pass)
	// DBから最新の同期情報を取得
	motodata, err := ses.db.GetData(path)
	snum := ses.db.GetSyncNumber(path)
	if err != nil {
		if snum < 0 {
			motodata = nil
		} else {
			// 初期同期じゃないのにデータの読み込みに失敗
			return http.StatusInternalServerError, err // 500
		}
	}
	// アイテムデータの読み込み
	itemdata, err := ses.db.GetItemData(path)
	if err != nil {
		itemdata = []byte("{}")
	}

	// 解析
	sync := &Sync2ch_v1{
		item: ItemMap_v1{
			ThreadMap: make(map[string]int),
			BoardMap:  make(map[string]int),
			DirMap:    make(map[string]int),
			GroupMap:  make(map[string]int),
			SyncNum:   snum,
		},
	}
	d1, d2, d3, err := sync.Merge(reqdata, itemdata, motodata)
	if err != nil {
		return http.StatusInternalServerError, err // 500
	}

	// 同期番号の保存
	err = ses.db.SetSyncNumber(path, sync.save.SyncNum)
	if err != nil {
		return http.StatusInternalServerError, err // 500
	}
	// データの保存
	err = ses.db.SetData(path, d1)
	if err != nil {
		return http.StatusInternalServerError, err // 500
	}
	// アイテムデータの保存
	err = ses.db.SetItemData(path, d2)
	if err != nil {
		return http.StatusInternalServerError, err // 500
	}

	// クライアントへ送信
	err = ses.send(d3)
	if err != nil {
		return http.StatusInternalServerError, err // 500
	}
	debugPrint(string(d3))

	return http.StatusOK, nil
}

func (ses *Session) statusCode(code int) {
	// ステータスコード出力
	ses.w.WriteHeader(code)
	g_log.Printf("%s - \"%s %s %s\" code:%d in:%d out:%d", ses.r.RemoteAddr, ses.r.Method, ses.r.RequestURI, ses.r.Proto, code, ses.r.ContentLength, ses.wbuf.Len())
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
		ses.wbuf.WriteString(xml.Header)
		ses.wbuf.Write(data)
	}
	return
}

// ちゃんとしたDBとかに後々対応できるように…

func (db *FileDB) GetSyncNumber(name string) int {
	p := db.createUserPath(name) + "/" + FILE_DB_NUMBER_NAME
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
	p := db.createUserPath(name) + "/" + FILE_DB_NUMBER_NAME
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
	p, err := db.createDataPath(name)
	if err == nil {
		data, err = ioutil.ReadFile(p)
	}
	return data, err
}

func (db *FileDB) SetData(name string, data []byte) error {
	p, err := db.createDataPath(name)
	if err == nil {
		err = db.checkPath(p)
		if err == nil {
			err = ioutil.WriteFile(p, data, 0744)
		}
	}
	return err
}

func (db *FileDB) GetItemData(name string) ([]byte, error) {
	var data []byte
	p, err := db.createItemDataPath(name)
	if err == nil {
		data, err = ioutil.ReadFile(p)
	}
	return data, err
}

func (db *FileDB) SetItemData(name string, data []byte) error {
	p, err := db.createItemDataPath(name)
	if err == nil {
		err = db.checkPath(p)
		if err == nil {
			err = ioutil.WriteFile(p, data, 0744)
		}
	}
	return err
}

func (db *FileDB) createDataPath(name string) (string, error) {
	num := db.GetSyncNumber(name)
	if num < 0 {
		return "", errors.New("sync number error")
	}
	p := fmt.Sprintf("%s/%s_%d.xml", db.createUserPath(name), FILE_DB_PREFIX, num)
	return p, nil
}

func (db *FileDB) createItemDataPath(name string) (string, error) {
	p := db.createUserPath(name) + "/" + FILE_ITEM_DB_NAME
	return p, nil
}

func (db *FileDB) createUserPath(name string) string {
	return db.root + "/" + name
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

/////////////////////////////////////////////////////////////////////
// Sync2ch Version1
/////////////////////////////////////////////////////////////////////
func (v1 *Sync2ch_v1) Merge(src1, src2, src3 []byte) (d1, d2, d3 []byte, reterr error) {
	// バイト列から変換
	reterr = xml.Unmarshal(src1, &v1.req)
	if reterr != nil {
		return
	}
	reterr = json.Unmarshal(src2, &v1.item)
	if reterr != nil {
		return
	}
	if src3 != nil {
		reterr = xml.Unmarshal(src3, &v1.load)
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
		return
	}
	d2, reterr = json.MarshalIndent(&v1.item, "", "\t")
	if reterr != nil {
		return
	}
	d3, reterr = xml.MarshalIndent(&v1.res, "", "\t")
	if reterr != nil {
		return
	}
	return
}

func (v1 *Sync2ch_v1) update() {
	v1.save = v1.load
	if v1.load.SyncNum == v1.req.SyncNum {
		// 情報の更新
		v1.req.TgMap = convertMap(v1.req.Tg)
		v1.createResponseUpdate()
		v1.mergeRequest()
		// アイテムの履歴を更新
		for _, it := range v1.req.Tg {
			if m, ok := v1.req.TgMap[it.Cate]; ok {
				(&v1.item).update(m)
			}
			(&v1.item).setGroup(it.Cate)
		}
	} else if v1.load.SyncNum > v1.req.SyncNum {
		// 同期
		v1.load.TgMap = convertMap(v1.load.Tg)
		v1.req.TgMap = convertMap(v1.req.Tg)
		v1.createResponseSync()
		v1.mergeRequestDiff()
	} else {
		// サーバの方が小さい番号になることはありえないはず…
		// 情報の更新
		v1.req.TgMap = convertMap(v1.req.Tg)
		v1.createResponseUpdate()
		v1.mergeRequest()
		// アイテムの履歴を更新
		for _, it := range v1.req.Tg {
			if m, ok := v1.req.TgMap[it.Cate]; ok {
				(&v1.item).update(m)
			}
			(&v1.item).setGroup(it.Cate)
		}
	}
	v1.save.ClientVer = v1.req.ClientVer
	v1.save.ClientName = v1.req.ClientName
	v1.save.Os = v1.req.Os
	v1.res.Result = "ok"
}

func (v1 *Sync2ch_v1) mergeRequest() {
	m := make(map[string]int)
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
}

func (v1 *Sync2ch_v1) mergeRequestDiff() {
	m := make(map[string]int)
	for i, it := range v1.save.Tg {
		m[it.Cate] = i
	}
	for _, it := range v1.req.Tg {
		if index, ok := m[it.Cate]; ok {
			sm := v1.save.Tg[index].convertGroupMap()
			if rm, rmok := v1.req.TgMap[it.Cate]; rmok {
				tg := &(v1.save.Tg[index])
				dir := Dir_v1{
					ThreadList: tg.ThreadList,
					BoardList:  tg.BoardList,
					DirList:    tg.DirList,
				}
				(&dir).merge(rm.diff(sm), v1.item)
				tg.ThreadList = dir.ThreadList
				tg.BoardList = dir.BoardList
				tg.DirList = dir.DirList
			}
		} else {
			if v1.item.isNewGroup(it.Cate) {
				// 新しいグループの場合無条件で追加
				// 後ろに伸びるだけだからインデックスは狂わないはず
				v1.save.Tg = append(v1.save.Tg, it)
				v1.item.setGroup(it.Cate)
			}
		}
	}
}

// リクエストのSyncNumberが同じ場合
// 全部none
func (v1 *Sync2ch_v1) createResponseUpdate() {
	add := []ThreadGroup_v1{}
	for key, re := range v1.req.TgMap {
		add = append(add, re.createUpdateResThreadGroup(key))
	}
	v1.res.Tg = add
	v1.updateSyncNumber()
}

// リクエストのSyncNumberが古い場合
// 同期する
func (v1 *Sync2ch_v1) createResponseSync() {
	add := []ThreadGroup_v1{}
	for key, re := range v1.req.TgMap {
		it, ok := v1.load.TgMap[key]
		if !ok {
			// 最新版には無いカテゴリーのもよう
			it = re
		}
		add = append(add, it.createSyncResThreadGroup(re, v1.item, key))
	}
	v1.res.Tg = add
	v1.updateSyncNumber()
}

func (v1 *Sync2ch_v1) updateSyncNumber() {
	// 番号の更新
	v1.save.SyncNum++
	v1.res.SyncNum = v1.save.SyncNum
}

func convertMap(tglist []ThreadGroup_v1) map[string]GroupMap_v1 {
	tgmap := make(map[string]GroupMap_v1)
	for _, it := range tglist {
		tgmap[it.Cate] = it.convertGroupMap()
	}
	return tgmap
}

func (tg ThreadGroup_v1) convertGroupMap() GroupMap_v1 {
	gm := GroupMap_v1{
		tm: make(map[string]Thread_v1),
		bm: make(map[string]Board_v1),
		dm: make(map[string]GroupMap_v1),
	}
	for _, it := range tg.ThreadList {
		gm.tm[it.Url] = it
	}
	for _, it := range tg.BoardList {
		gm.bm[it.Url] = it
	}
	for _, it := range tg.DirList {
		gm.dm[it.Name] = (ThreadGroup_v1{
			ThreadList: it.ThreadList,
			BoardList:  it.BoardList,
			DirList:    it.DirList,
		}).convertGroupMap()
	}
	return gm
}

func (req GroupMap_v1) createUpdateResDir(name string) Dir_v1 {
	data := Dir_v1{
		Name: name,
	}
	t, b, d := req.createUpdateResList()
	(&data).add(t, b, d)
	return data
}

func (req GroupMap_v1) createUpdateResThreadGroup(key string) ThreadGroup_v1 {
	data := ThreadGroup_v1{
		Cate: key,
	}
	t, b, d := req.createUpdateResList()
	(&data).add(t, b, d)
	return data
}

func (req GroupMap_v1) createUpdateResList() (t []Thread_v1, b []Board_v1, d []Dir_v1) {
	for _, it := range req.tm {
		t = append(t, Thread_v1{
			Url:    it.Url,
			Status: "n",
		})
	}
	for _, it := range req.bm {
		b = append(b, it)
	}
	for na, it := range req.dm {
		d = append(d, it.createUpdateResDir(na))
	}
	return
}

func (t Thread_v1) createSyncRes(req *Thread_v1) Thread_v1 {
	ret := Thread_v1{
		Url: t.Url,
	}
	if req == nil {
		ret.Status = "a"
	} else if req.Read != t.Read || req.Now != t.Now || req.Count != t.Count {
		ret.Status = "u"
	} else {
		ret.Status = "n"
	}

	if ret.Status == "a" || ret.Status == "u" {
		ret.Title = t.Title
		if t.Read > 0 {
			ret.Read = t.Read
		} else {
			ret.Read = 1
		}
		if t.Now > 0 {
			ret.Now = t.Now
		} else {
			ret.Now = 1
		}
		ret.Count = t.Count
	}
	return ret
}

func (gm GroupMap_v1) createSyncResDir(req *GroupMap_v1, im ItemMap_v1, name string) Dir_v1 {
	var t []Thread_v1
	var b []Board_v1
	var d []Dir_v1
	data := Dir_v1{
		Name: name,
	}

	if req == nil {
		// リクエストには無いフォルダ
		for _, it := range gm.tm {
			t = append(t, it.createSyncRes(nil))
		}
		for _, it := range gm.bm {
			b = append(b, it)
		}
		for na, it := range gm.dm {
			d = append(d, it.createSyncResDir(nil, im, na))
		}
	} else {
		t, b, d = gm.createSyncResList(*req, im)
	}

	(&data).add(t, b, d)
	return data
}

func (gm GroupMap_v1) createSyncResThreadGroup(req GroupMap_v1, im ItemMap_v1, key string) ThreadGroup_v1 {
	data := ThreadGroup_v1{
		Cate: key,
	}
	t, b, d := gm.createSyncResList(req, im)
	(&data).add(t, b, d)
	return data
}

func (gm GroupMap_v1) createSyncResList(req GroupMap_v1, im ItemMap_v1) ([]Thread_v1, []Board_v1, []Dir_v1) {
	// サーバ側のアイテム
	t, b, d := gm.syncResItems(req, im)
	// リクエスト側にのみ存在するアイテム
	t2, b2, d2 := req.diff(gm).syncResItemsReqOnly(im)
	t = append(t, t2...)
	b = append(b, b2...)
	// TODO:階層を維持してマージする必要がある
	d = append(d, d2...)
	return t, b, d
}

func (di *Dir_v1) merge(gm GroupMap_v1, im ItemMap_v1) {
	m := make(map[string]int)
	for i, it := range di.DirList {
		m[it.Name] = i
	}

	for url, it := range gm.tm {
		if im.isNewThread(url) {
			di.ThreadList = append(di.ThreadList, it)
			im.setThread(url)
		}
	}
	for url, it := range gm.bm {
		if im.isNewBoard(url) {
			di.BoardList = append(di.BoardList, it)
			im.setBoard(url)
		}
	}
	for name, it := range gm.dm {
		if im.isNewDir(name) {
			if index, ok := m[name]; ok {
				(&(di.DirList[index])).merge(it, im)
			} else {
				dir := Dir_v1{Name: name}
				(&dir).merge(it, im)
				di.DirList = append(di.DirList, dir)
			}
			im.setDir(name)
		}
	}
}

func (tg *ThreadGroup_v1) add(t []Thread_v1, b []Board_v1, d []Dir_v1) {
	tg.ThreadList = nil
	tg.BoardList = nil
	tg.DirList = nil

	if len(t) > 0 {
		tg.ThreadList = t
	}
	if len(b) > 0 {
		tg.BoardList = b
	}
	if len(d) > 0 {
		tg.DirList = d
	}
}

func (di *Dir_v1) add(t []Thread_v1, b []Board_v1, d []Dir_v1) {
	di.ThreadList = nil
	di.BoardList = nil
	di.DirList = nil

	if len(t) > 0 {
		di.ThreadList = t
	}
	if len(b) > 0 {
		di.BoardList = b
	}
	if len(d) > 0 {
		di.DirList = d
	}
}

func (gm GroupMap_v1) syncResItems(req GroupMap_v1, im ItemMap_v1) (t []Thread_v1, b []Board_v1, d []Dir_v1) {
	for url, it := range gm.tm {
		if re, ok := req.tm[url]; ok {
			t = append(t, it.createSyncRes(&re))
		} else {
			t = append(t, it.createSyncRes(nil))
		}
	}
	for _, it := range gm.bm {
		b = append(b, it)
	}
	for name, it := range gm.dm {
		if re, ok := req.dm[name]; ok {
			d = append(d, it.createSyncResDir(&re, im, name))
		} else {
			d = append(d, it.createSyncResDir(nil, im, name))
		}
	}
	return
}

func (gm GroupMap_v1) syncResItemsReqOnly(im ItemMap_v1) (t []Thread_v1, b []Board_v1, d []Dir_v1) {
	for url, it := range gm.tm {
		if im.isNewThread(url) {
			t = append(t, it.createSyncRes(nil))
		}
	}
	for url, it := range gm.bm {
		if im.isNewBoard(url) {
			b = append(b, it)
		}
	}
	for name, it := range gm.dm {
		// TODO:階層を考慮する必要があるかも
		if im.isNewDir(name) {
			d = append(d, it.createSyncResDir(nil, im, name))
		}
	}
	return
}

func (m GroupMap_v1) diff(src GroupMap_v1) GroupMap_v1 {
	tmp := m.clone()
	for url, _ := range src.tm {
		delete(tmp.tm, url)
	}
	for url, _ := range src.bm {
		delete(tmp.bm, url)
	}
	for name, it := range src.dm {
		if re, ok := tmp.dm[name]; ok {
			di := re.diff(it)
			if di.isEmpty() {
				delete(tmp.dm, name)
			} else {
				tmp.dm[name] = di
			}
		}
	}
	return tmp
}

func (m GroupMap_v1) clone() GroupMap_v1 {
	ret := GroupMap_v1{
		tm: make(map[string]Thread_v1),
		bm: make(map[string]Board_v1),
		dm: make(map[string]GroupMap_v1),
	}
	for key, it := range m.tm {
		ret.tm[key] = it
	}
	for key, it := range m.bm {
		ret.bm[key] = it
	}
	for key, it := range m.dm {
		ret.dm[key] = it.clone()
	}
	return ret
}

func (m GroupMap_v1) isEmpty() bool {
	return len(m.tm) == 0 && len(m.bm) == 0 && len(m.dm) == 0
}

func (s *ItemMap_v1) update(m GroupMap_v1) {
	for key, _ := range m.tm {
		s.setThread(key)
	}
	for key, _ := range m.bm {
		s.setBoard(key)
	}
	for key, _ := range m.dm {
		s.setDir(key)
		// TODO:再帰的にアップデートする必要があるかも
		//s.update(it)
	}
}

func (s *ItemMap_v1) setThread(url string) { s.ThreadMap[url] = s.SyncNum }
func (s *ItemMap_v1) setBoard(url string)  { s.BoardMap[url] = s.SyncNum }
func (s *ItemMap_v1) setDir(name string)   { s.DirMap[name] = s.SyncNum }
func (s *ItemMap_v1) setGroup(cate string) { s.GroupMap[cate] = s.SyncNum }

func (s ItemMap_v1) isNewThread(url string) bool {
	n, ok := s.ThreadMap[url]
	return s.checkNewItem(n, ok)
}

func (s ItemMap_v1) isNewBoard(url string) bool {
	n, ok := s.BoardMap[url]
	return s.checkNewItem(n, ok)
}

func (s ItemMap_v1) isNewDir(name string) bool {
	n, ok := s.DirMap[name]
	return s.checkNewItem(n, ok)
}

func (s ItemMap_v1) isNewGroup(cate string) bool {
	n, ok := s.GroupMap[cate]
	return s.checkNewItem(n, ok)
}

func (s ItemMap_v1) checkNewItem(n int, ok bool) (ret bool) {
	ret = false
	if ok {
		if n <= (s.SyncNum - 9) {
			ret = true
		}
	} else {
		ret = true
	}
	return
}

