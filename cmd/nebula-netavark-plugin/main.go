package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"time"

	"github.com/bensku/nebula-netavark/proto"
)

func main() {
	// Make sure service is available before handling anything else
	// If it isn't, operator probably screwed up systemd unit ordering...
	// in which case best we can do is force pod/container restart until it starts up
	socketPath, ok := os.LookupEnv("NEBULA_CONTROL_SOCKET")
	if !ok {
		socketPath = "/run/nebula/netavark.sock"
	}
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		exitError(err.Error())
	}
	defer conn.Close()

	// Send netavark payload + args to service, exiting on any error
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		exitError(err.Error())
	}
	err = json.NewEncoder(conn).Encode(proto.PluginMessage{Args: os.Args, Raw: payload})
	if err != nil {
		exitError(err.Error())
	}

	// Forward reply to stdout as-is
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	reply, err := io.ReadAll(conn)
	if err != nil {
		exitError(err.Error())
	}
	os.Stdout.Write(reply)
}

func exitError(msg string) {
	if err := json.NewEncoder(os.Stdout).Encode(proto.Error{Error: msg}); err != nil {
		panic(err)
	}
	os.Exit(1)
}
