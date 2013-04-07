// sync2chの挙動調査
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
)

type Thread_v1 struct {
	Url   string `xml:"url,attr"`
	Title string `xml:"title,attr"`
	Read  int    `xml:"read,attr"`
	Now   int    `xml:"now,attr"`
	Count int    `xml:"count,attr"`
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

type Request_v1 struct {
	XMLName       xml.Name         `xml:"sync2ch_request"`
	SyncNum       int              `xml:"sync_number,attr"`
	ClientVer     string           `xml:"client_version,attr"`
	ClientName    string           `xml:"client_name,attr"`
	Os            string           `xml:"os,attr"`
	Tg            []ThreadGroup_v1 `xml:"thread_group"`
	SyncNumOffset int              `xml:"-"`
	Subject       string           `xml:"-"`
}

type Response_v1 struct {
	XMLName xml.Name `xml:"sync2ch_response"`
	SyncNum int      `xml:"sync_number,attr"`
}

var g_XMLName xml.Name = xml.Name{
	Space: "",
	Local: "sync2ch_request",
}

func main() {
	new_cate := "favo" + strconv.Itoa(rand.Intn(1000000))
	g_Thread1 := createThread(1)
	g_Thread2 := createThread(2)
	g_Thread3 := createThread(3)
	g_Thread4 := createThread(4)
	g_Thread5 := createThread(5)
	g_Thread6 := createThread(6)
	g_Thread7 := createThread(7)
	g_Thread1_2 := g_Thread1
	g_Thread2_2 := g_Thread2
	g_Thread3_2 := g_Thread3
	g_Thread1_2.Read = 2
	g_Thread2_2.Now = 2
	g_Thread3_2.Count = 1001
	g_Board1 := Board_v1{
		Url:   "http://hoge.2ch.net/aaa/",
		Title: "board1",
	}
	g_Board2 := Board_v1{
		Url:   "http://hoge.2ch.net/bbb/",
		Title: "board2",
	}
	g_Dir1 := Dir_v1{
		Name:       "お気に入り1",
		ThreadList: []Thread_v1{g_Thread5},
		BoardList:  []Board_v1{g_Board1},
	}
	g_Dir2 := Dir_v1{
		Name:       "お気に入り2",
		ThreadList: []Thread_v1{g_Thread5},
		BoardList:  []Board_v1{g_Board1},
		DirList:    []Dir_v1{g_Dir1},
	}
	set_req_moto := Request_v1{ // 元に戻す
		Subject: "元に戻す",
		Tg: []ThreadGroup_v1{
			ThreadGroup_v1{
				Cate: "open",
				ThreadList: []Thread_v1{
					g_Thread1,
					g_Thread2,
					g_Thread3,
				},
			},
			ThreadGroup_v1{
				Cate:      "favorite",
				BoardList: []Board_v1{g_Board1},
				DirList:   []Dir_v1{g_Dir1},
			},
		},
	}
	get_req_data := Request_v1{
		Subject: "情報取得用",
		SyncNumOffset: -2,
		Tg: []ThreadGroup_v1{
			ThreadGroup_v1{
				Cate: "open",
				ThreadList: []Thread_v1{
					g_Thread1,
					g_Thread2,
					g_Thread3,
				},
			},
			ThreadGroup_v1{
				Cate:      "favorite",
				BoardList: []Board_v1{g_Board1},
				DirList:   []Dir_v1{g_Dir1},
			},
		},
	}


	reqlist := []Request_v1{
		Request_v1{		// 初回
			Subject: "初回",
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{Cate: "open"},
				ThreadGroup_v1{Cate: "favorite"},
			},
		},
		set_req_moto,	// 元に戻す
		Request_v1{		// スレッド追加
			Subject: "スレッド追加",
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread3,
						g_Thread4,
						g_Thread5,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
			},
		},
		get_req_data,	// 情報取得
		set_req_moto,	// 元に戻す
		Request_v1{		// SyncNumが2つ小さい、新しいカテゴリの追加
			Subject: "SyncNumが2つ小さい、新しいカテゴリの追加",
			SyncNumOffset: -2,
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread3,
						g_Thread4,
						g_Thread5,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
				ThreadGroup_v1{
					Cate:      new_cate,
					BoardList: []Board_v1{
						g_Board1,
						g_Board2,
					},
					DirList:   []Dir_v1{g_Dir2},
				},
			},
		},
		Request_v1{
			Subject: "情報取得用、新しいカテゴリに対応",
			SyncNumOffset: -2,
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread3,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
				ThreadGroup_v1{Cate: new_cate},
			},
		},
		set_req_moto,	// 元に戻す
		Request_v1{		// SyncNumが1つ大きい
			Subject: "SyncNumが1つ大きい",
			SyncNumOffset: 1,
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread3,
						g_Thread4,
						g_Thread5,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
			},
		},
		get_req_data,	// 情報取得
		set_req_moto,	// 元に戻す
		Request_v1{		// SyncNumが1つ小さい、昔追加したスレッドの追加
			Subject: "SyncNumが1つ小さい、昔追加したスレッドの追加",
			SyncNumOffset: -1,
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread3,
						g_Thread4,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
			},
		},
		get_req_data,	// 情報取得
		set_req_moto,	// 元に戻す
		Request_v1{		// SyncNumが2つ小さい、昔追加したスレッドの追加
			Subject: "SyncNumが2つ小さい、昔追加したスレッドの追加",
			SyncNumOffset: -2,
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread3,
						g_Thread4,
						g_Thread5,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
			},
		},
		get_req_data,	// 情報取得
		set_req_moto,	// 元に戻す
		Request_v1{		// SyncNumが2つ小さい、昔追加したスレッドの追加
			Subject: "SyncNumが2つ小さい、新しいスレッドの追加",
			SyncNumOffset: -2,
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread3,
						g_Thread4,
						g_Thread5,
						g_Thread6,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
			},
		},
		get_req_data,	// 情報取得
		set_req_moto,	// 元に戻す
		Request_v1{		// SyncNumが2つ小さい、昔追加したスレッドの追加
			Subject: "SyncNumが2つ小さい、新しいスレッドの追加、削除",
			SyncNumOffset: -2,
			Tg: []ThreadGroup_v1{
				ThreadGroup_v1{
					Cate: "open",
					ThreadList: []Thread_v1{
						g_Thread1,
						g_Thread2,
						g_Thread6,
						g_Thread7,
					},
				},
				ThreadGroup_v1{
					Cate:      "favorite",
					BoardList: []Board_v1{g_Board1},
					DirList:   []Dir_v1{g_Dir1},
				},
			},
		},
		get_req_data,	// 情報取得
	}

	sn := 0
	var id string
	var pass string
	if len(os.Args) == 3 {
		id = os.Args[1]
		pass = os.Args[2]
	} else {
		fmt.Println("$./ver1test.exe id pass")
		os.Exit(1)
	}
	for i, req := range reqlist {
		req.XMLName = g_XMLName
		req.SyncNum = sn + req.SyncNumOffset
		req.ClientVer = "0.0.1b"
		req.ClientName = "ver1test.go"
		req.Os = "Lindows"

		send, _ := xml.MarshalIndent(&req, "", "\t")
		data, err := sync(id, pass, send)
		if err != nil {
			break
		}

		var resp Response_v1
		xml.Unmarshal(data, &resp)
		sn = resp.SyncNum

		ioutil.WriteFile(fmt.Sprintf("%02d_%s_req.xml", i, req.Subject), send, 0777)
		ioutil.WriteFile(fmt.Sprintf("%02d_%s_res.xml", i, req.Subject), data, 0777)
	}
}

func sync(id, pass string, data []byte) ([]byte, error) {
	body := bytes.NewBuffer(data)
	client := &http.Client{}
	req, err := http.NewRequest("POST", "http://sync2ch.com/api/sync1", body)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":"+pass)))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rd, err := ioutil.ReadAll(resp.Body)
	return rd, err
}

func createThread(no int) Thread_v1 {
	sin := 1365000000 + rand.Intn(10000000)
	return Thread_v1{
		Url:   "http://hoge.2ch.net/read.cgi/test/aaa/" + strconv.Itoa(sin) + "/",
		Title: fmt.Sprintf("test%02d", no),
		Read:  1,
		Now:   1,
		Count: 100 + no,
	}
}
