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
	os.Exit(run(socketPath, os.Args, os.Stdin, os.Stdout))
}

// run forwards the daemon response unchanged and reports failures to Netavark
// using both JSON on stdout and a nonzero process exit status.
func run(socketPath string, args []string, stdin io.Reader, stdout io.Writer) int {
	conn, err := net.DialTimeout("unix", socketPath, 10*time.Second)
	if err != nil {
		return writeError(stdout, err)
	}
	defer conn.Close()

	// Send netavark payload + args to service, exiting on any error
	payload, err := io.ReadAll(stdin)
	if err != nil {
		return writeError(stdout, err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return writeError(stdout, err)
	}
	err = json.NewEncoder(conn).Encode(proto.PluginMessage{Args: args, Raw: payload})
	if err != nil {
		return writeError(stdout, err)
	}

	// Forward reply to stdout as-is
	reply, err := io.ReadAll(conn)
	if err != nil {
		return writeError(stdout, err)
	}
	if _, err := stdout.Write(reply); err != nil {
		return 1
	}
	var failure struct {
		Error *string `json:"error"`
	}
	if json.Unmarshal(reply, &failure) == nil && failure.Error != nil {
		return 1
	}
	return 0
}

func writeError(stdout io.Writer, err error) int {
	_ = json.NewEncoder(stdout).Encode(proto.Error{Error: err.Error()})
	return 1
}
