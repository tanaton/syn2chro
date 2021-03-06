#ver1メモ
sync2ch本家のver1の挙動を適当に調べたメモ

_おかしいところが多い。後で直す_

##用語
* アイテム → スレッド、板、フォルダのこと

##同期方法
以下のパターンに分かれる。
1. リクエストのsync numberがサーバのsync numberと同じ、もしくは1つ小さい
2. リクエストのsync numberがサーバのsync numberより2以上小さい
3. リクエストのsync numberがサーバのsync numberより大きい

###リクエストのsync numberがサーバのsync numberと同じ、もしくは1つ小さい
サーバに保存された情報をリクエストの情報に置き換える。

ただしリクエストされたカテゴリのみ。他のカテゴリはそのまま残す。

レスポンスはリクエストの情報を更新無しとして設定する。

sync numberをインクリメントする。

####初回同期の場合
サーバ、リクエスト共に初回の同期の場合、レスポンスのsync numberは2になる。

###リクエストのsync numberがサーバのsync numberより2以上小さい
リクエストに含まれるアイテムとサーバに保存されているアイテムをマージしたレスポンスを返す。

リクエストのスレッドに以下の変更が含まれる場合、ステータスを更新にする。
* titleが変化している場合
* readが変化している場合
* countが変化している場合

リクエストに含まれないデータはステータスを追加にする。

リクエストに含まれ、特定の変更が含まれていない場合、ステータスは変更無しにする。

リクエストの情報は基本的にはサーバに保存されない。

リクエストに存在し、サーバに存在しない情報はレスポンスには含まれない。

sync numberはカウントされない。（要調査）

####リクエストに一度も同期したことがないアイテムが含まれている場合
一度同期したアイテムは基本的には完全に削除されず、内部で保持している。

そのため、一度も同期していないアイテムは特別扱いし、sync numberが小さい場合でもサーバに保存する。

新しくサーバに保存したアイテムはステータスを変更無しにする。

sync numberをインクリメントする。

###リクエストのsync numberがサーバのsync numberより大きい場合
リクエストのsync numberがサーバのsync numberより2以上小さいと同じ？

##想定されている使い方の想定
sync2chの同期方法から、想定されている使い方を考える

###正しいパターン
想定されている使い方は以下のようになると思う
1. 端末1で同期する。最新の状態が端末1に反映される
2. 端末1でスレッドを閲覧する
3. 閲覧した情報を保存するため端末1を同期する
4. 端末2で同期する。3で保存した端末1の状態が端末2に同期される
5. 端末2でスレッドを閲覧する
6. 閲覧した情報を保存するため端末2を同期する
以下繰り返し

基本的に使う前に同期して、使い終わったら同期する必要がある

###間違ったパターン
たぶん上手く動かないパターンは以下になる

####複数台の同時使用
複数台の端末を同時に使用する場合
1. 端末1で同期する
2. 端末1でスレッドを閲覧する
3. 端末2で同期する。端末1の情報は後で同期するつもり
4. 端末2でスレッドを閲覧する
5. 閲覧した情報を保存するため端末2を同期する
6. 端末2で閲覧した情報を取得するため端末1を同期する

6で同期した際、2で追加された情報は消えてなくなる

