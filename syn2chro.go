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
	Name            string `json:"ユーザー名"`
	Pass            string `json:"パスワード"`
	Addr            string `json:"アドレス"`
	Port            int    `json:"ポート番号"`
	ReadTimeoutSec  int    `json:"読み込みタイムアウト秒"`
	WriteTimeoutSec int    `json:"書き込みタイムアウト秒"`
	LogFilePath     string `json:"ログファイルパス"`
	DBPath          string `json:"データベースファイルのルートパス"`
	DebugPrint      bool   `json:"デバッグ出力"`
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
	SyncNum int `json:"sync_number"`
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
	ThreadMap  map[string]int `json:"ThreadMap,omitempty"`
	BoardMap   map[string]int `json:"BoardMap,omitempty"`
	DirMap     map[string]int `json:"DirMap,omitempty"`
	SyncNum    int            `json:"-"`
	ReqSyncNum int            `json:"-"`
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

type Group_v1 struct {
	ThreadList []Thread_v1 `xml:"thread"`
	BoardList  []Board_v1  `xml:"board"`
	DirList    []Dir_v1    `xml:"dir"` // 入れ子にできる
}

type Dir_v1 struct {
	Name string `xml:"name,attr"`
	Group_v1
}

type ThreadGroup_v1 struct {
	Cate string `xml:"category,attr"`
	Group_v1
}

type GroupMap_v1 struct {
	tm map[string]Thread_v1
	bm map[string]Board_v1
	dm map[string]GroupMap_v1
}

type Request_v1 struct {
	XMLName    xml.Name         `xml:"sync2ch_request"`
	SyncNum    int              `xml:"sync_number,attr"`
	ClientVer  string           `xml:"client_version,attr"`
	ClientName string           `xml:"client_name,attr"`
	Os         string           `xml:"os,attr"`
	Tg         []ThreadGroup_v1 `xml:"thread_group"`
}

type Response_v1 struct {
	XMLName xml.Name         `xml:"sync2ch_response"`
	Result  string           `xml:"result,attr"`
	SyncNum int              `xml:"sync_number,attr"`
	Tg      []ThreadGroup_v1 `xml:"thread_group"`
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
		Addr:           fmt.Sprintf("%s:%d", c.Addr, c.Port),
		Handler:        myHandler,
		ReadTimeout:    time.Duration(c.ReadTimeoutSec) * time.Second,
		WriteTimeout:   time.Duration(c.WriteTimeoutSec) * time.Second,
		MaxHeaderBytes: 1024 * 1024,
	}
	g_log.Printf("listen start %s:%d\n", c.Addr, c.Port)
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
	return dbn.SyncNum
}

func (db *FileDB) SetSyncNumber(name string, num int) (reterr error) {
	p := db.createUserPath(name) + "/" + FILE_DB_NUMBER_NAME
	dbn := FileDBNumber{
		SyncNum: num,
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
	v1.item.SyncNum = v1.load.SyncNum
	v1.item.ReqSyncNum = v1.req.SyncNum
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

	sync := false
	// 同期
	if v1.req.SyncNum >= v1.load.SyncNum {
		sync = true
		v1.mergeRequestSync()
		v1.createResponseSync()
	} else {
		sync = v1.mergeRequest()
		v1.createResponse()
	}
	if sync {
		v1.updateSyncNumber()
	}
	v1.save.ClientVer = v1.req.ClientVer
	v1.save.ClientName = v1.req.ClientName
	v1.save.Os = v1.req.Os
	v1.res.Result = "ok"
}

// 保存情報の更新
func (v1 *Sync2ch_v1) mergeRequestSync() {
	m := make(map[string]int)
	for i, it := range v1.save.Tg {
		m[it.Cate] = i
	}
	for _, it := range v1.req.Tg {
		if index, ok := m[it.Cate]; ok {
			// まるごと更新
			v1.save.Tg[index] = it
		} else {
			// 新しいグループの場合無条件で追加
			// 後ろに伸びるだけだからインデックスは狂わないはず
			v1.save.Tg = append(v1.save.Tg, it)
		}
	}
}

// 応答の生成
func (v1 *Sync2ch_v1) createResponseSync() {
	add := []ThreadGroup_v1{}
	rm := convertMap(v1.req.Tg)
	for key, re := range rm {
		// リクエストとレスポンスは同じ
		add = append(add, ThreadGroup_v1{
			Cate:     key,
			Group_v1: re.createUpdateResGroup(),
		})
		// アイテムDB更新
		v1.item.update(re)
	}
	v1.res.Tg = add
}

// 保存情報の更新
func (v1 *Sync2ch_v1) mergeRequest() (ret bool) {
	ret = false
	m := make(map[string]int)
	for i, it := range v1.save.Tg {
		m[it.Cate] = i
	}
	for _, it := range v1.req.Tg {
		rm := it.convertMap()
		index, ok := m[it.Cate]
		if v1.item.check(rm) {
			if ok {
				// まるごと更新
				v1.save.Tg[index] = it
			} else {
				// 後ろに伸びるだけだからインデックスは狂わないはず
				v1.save.Tg = append(v1.save.Tg, it)
			}
			ret = true
		} else if !ok {
			// 新しいグループの場合無条件で追加
			v1.save.Tg = append(v1.save.Tg, it)
			ret = true
		}
	}
	return
}

// 応答の生成
func (v1 *Sync2ch_v1) createResponse() {
	add := []ThreadGroup_v1{}
	rm := convertMap(v1.req.Tg)
	lm := convertMap(v1.load.Tg)
	for key, re := range rm {
		if v1.item.check(re) {
			// 今までに追加されたことの無い新しいアイテム
			// リクエストとレスポンスは同じ
			add = append(add, ThreadGroup_v1{
				Cate:     key,
				Group_v1: re.createUpdateResGroup(),
			})
		} else {
			// 昔からあるアイテムの場合
			it, ok := lm[key]
			if !ok {
				// 最新版には無いカテゴリーのもよう
				it = re
			}
			add = append(add, ThreadGroup_v1{
				Cate:     key,
				Group_v1: it.createSyncResList(re, v1.item),
			})
		}
		// アイテムDB更新
		v1.item.update(re)
	}
	v1.res.Tg = add
}

func (v1 *Sync2ch_v1) updateSyncNumber() {
	// 番号の更新
	v1.save.SyncNum++
	v1.res.SyncNum = v1.save.SyncNum
}

func convertMap(tglist []ThreadGroup_v1) map[string]GroupMap_v1 {
	tgmap := make(map[string]GroupMap_v1)
	for _, it := range tglist {
		tgmap[it.Cate] = it.Group_v1.convertMap()
	}
	return tgmap
}

func (tg Group_v1) convertMap() GroupMap_v1 {
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
		gm.dm[it.Name] = it.Group_v1.convertMap()
	}
	return gm
}

func (req GroupMap_v1) createUpdateResGroup() (data Group_v1) {
	for _, it := range req.tm {
		data.ThreadList = append(data.ThreadList, Thread_v1{
			Url:    it.Url,
			Status: "n",
		})
	}
	for _, it := range req.bm {
		data.BoardList = append(data.BoardList, it)
	}
	for name, it := range req.dm {
		data.DirList = append(data.DirList, Dir_v1{
			Name:     name,
			Group_v1: it.createUpdateResGroup(),
		})
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

func (gm GroupMap_v1) createSyncResList(req GroupMap_v1, im ItemMap_v1) Group_v1 {
	// サーバ側のアイテム
	gp := gm.syncResItems(req, im)
	// リクエスト側にのみ存在するアイテム
	gp2 := req.diff(gm).syncResItemsReqOnly(im)
	gp.ThreadList = append(gp.ThreadList, gp2.ThreadList...)
	gp.BoardList = append(gp.BoardList, gp2.BoardList...)
	gp.DirList = append(gp.DirList, gp2.DirList...)
	return gp
}

func (gm GroupMap_v1) syncResItems(req GroupMap_v1, im ItemMap_v1) (gp Group_v1) {
	for url, it := range gm.tm {
		if re, ok := req.tm[url]; ok {
			gp.ThreadList = append(gp.ThreadList, it.createSyncRes(&re))
		} else {
			gp.ThreadList = append(gp.ThreadList, it.createSyncRes(nil))
		}
	}
	for _, it := range gm.bm {
		gp.BoardList = append(gp.BoardList, it)
	}
	for name, it := range gm.dm {
		if re, ok := req.dm[name]; ok {
			gp.DirList = append(gp.DirList, Dir_v1{
				Name:     name,
				Group_v1: it.syncResItems(re, im),
			})
		} else {
			gp.DirList = append(gp.DirList, Dir_v1{
				Name:     name,
				Group_v1: it.syncResItemsReqOnly(im),
			})
		}
	}
	return
}

func (gm GroupMap_v1) syncResItemsReqOnly(im ItemMap_v1) (gp Group_v1) {
	for _, it := range gm.tm {
		gp.ThreadList = append(gp.ThreadList, it.createSyncRes(nil))
	}
	for _, it := range gm.bm {
		gp.BoardList = append(gp.BoardList, it)
	}
	for name, it := range gm.dm {
		gp.DirList = append(gp.DirList, Dir_v1{
			Name:     name,
			Group_v1: it.syncResItemsReqOnly(im),
		})
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
	var udfunc func(GroupMap_v1)
	udfunc = func(m GroupMap_v1) {
		for key, it := range m.dm {
			s.setDir(key)
			udfunc(it)
		}
	}
	udfunc(m)
}

func (s ItemMap_v1) check(m GroupMap_v1) bool {
	for key, _ := range m.tm {
		if s.isNewThread(key) {
			return true
		}
	}
	for key, _ := range m.bm {
		if s.isNewBoard(key) {
			return true
		}
	}
	var udfunc func(GroupMap_v1) bool
	udfunc = func(m GroupMap_v1) bool {
		for key, it := range m.dm {
			s.isNewDir(key)
			if udfunc(it) {
				return true
			}
		}
		return false
	}
	return udfunc(m)
}

func (s *ItemMap_v1) setThread(url string) { s.setItem(s.ThreadMap, url) }
func (s *ItemMap_v1) setBoard(url string)  { s.setItem(s.BoardMap, url) }
func (s *ItemMap_v1) setDir(name string)   { s.setItem(s.DirMap, name) }
func (s *ItemMap_v1) setItem(im map[string]int, key string) {
	if _, ok := im[key]; !ok {
		im[key] = s.ReqSyncNum
	}
}

func (s ItemMap_v1) isNewThread(url string) bool { return s.checkNewItem(s.ThreadMap, url) }
func (s ItemMap_v1) isNewBoard(url string) bool  { return s.checkNewItem(s.BoardMap, url) }
func (s ItemMap_v1) isNewDir(name string) bool   { return s.checkNewItem(s.DirMap, name) }
func (s ItemMap_v1) checkNewItem(im map[string]int, key string) (ret bool) {
	ret = false
	if _, ok := im[key]; !ok {
		// 新規追加
		ret = true
	}
	return
}
