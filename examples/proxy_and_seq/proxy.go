package main

import (
	tp "github.com/henrylee2cn/teleport"
	"github.com/henrylee2cn/teleport/plugin"
)

func main() {
	cli := tp.NewPeer(tp.PeerConfig{RedialTimes: 3})
	defer cli.Close()
	sess, err := cli.Dial(":9090")
	if err != nil {
		tp.Fatalf("%v", err)
	}

	srv := tp.NewPeer(
		tp.PeerConfig{
			ListenAddress: ":8080",
		},
		plugin.Proxy(sess),
	)
	srv.ListenAndServe()
}
