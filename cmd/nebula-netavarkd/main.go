package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bensku/nebula-netavark/network"
	"github.com/bensku/nebula-netavark/proto"
)

func main() {
	socketPath := flag.String("socket", "/run/nebula/netavark.sock", "Unix socket path for netavark plugin calls")
	configPath := flag.String("config", "/etc/containers/nebula", "Directory containing nebula network configuration")
	statePath := flag.String("state", "/var/nebula-netavark/state", "Directory for persistent state")
	flag.Parse()

	// Make sure we can create the socket
	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o700); err != nil {
		slog.Error("failed to create API socket dir", "path", filepath.Dir(*socketPath), "error", err)
		os.Exit(1)
	}
	stat, err := os.Stat(*socketPath)
	if err == nil {
		if stat.Mode().Type() == fs.ModeSocket {
			// Clear old socket
			if err := os.Remove(*socketPath); err != nil {
				slog.Error("socket already exists", "path", *socketPath)
				os.Exit(1)
			}
		} else {
			// Not socket, don't remove arbitrary files!
			slog.Error("cannot create socket, another file at path", "path", *socketPath)
			os.Exit(1)
		}
	} // else: hopefully the file doesn't exist; otherwise, Listen will fail

	// Interrupt on signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "unix", *socketPath)
	if err != nil {
		slog.Error("failed to bind API socket", "path", *socketPath, "error", err)
		os.Exit(1)
	}
	defer lis.Close()

	go func() { <-ctx.Done(); lis.Close() }()

	// Start net manager
	netmgr, err := network.LoadManager(*configPath, *statePath)
	if err != nil {
		slog.Error("failed to start netmgr", "error", err)
		os.Exit(1)
	}

	// Tell systemd we're ready to serve connections
	notifyReady()

	// Handle for incoming messages!
	for {
		conn, err := lis.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("netavark socket accept failed", "error", err)
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

		msg := proto.PluginMessage{}
		if err := json.NewDecoder(io.LimitReader(conn, 5*1024*1024)).Decode(&msg); err != nil {
			slog.Warn("failed to parse netavark payload")
			replyError(conn, err.Error(), "")
			_ = conn.Close()
			continue
		}

		go handleMessage(netmgr, conn, msg)
	}

}

func handleMessage(netmgr *network.Manager, conn net.Conn, msg proto.PluginMessage) {
	// If one Nebula crashes, don't crash all others!
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered from panic", "recover", r)
			replyError(conn, "panic in handleMessage, see logs", "")
		}
	}()

	if len(msg.Args) < 2 {
		replyError(conn, "internal error: too few arguments", "")
		return
	}

	cmd := msg.Args[1]
	switch cmd {
	case "create":
		// We don't change anything in network config, just echo it all back
		conn.Write(msg.Raw)
	case "setup":
		if len(msg.Args) < 3 {
			replyError(conn, "internal error: too few arguments", "")
			return
		}
		netns := msg.Args[2]
		req := proto.NetworkPluginExec{}
		err := json.Unmarshal(msg.Raw, &req)
		if err != nil {
			replyError(conn, err.Error(), "")
			return
		}

		// Connect endpoint to given netns and track it in case we need to restart
		if err := netmgr.Up(req.ContainerName, netns); err != nil {
			replyError(conn, err.Error(), req.ContainerName)
			return
		}

		reply(conn, proto.StatusBlock{
			// TODO fetch these from Nebula config somehow
			// Neither netavark or Podman care, but these show up in e.g. podman inspect
			Interfaces: map[string]proto.NetInterface{
				"footun": {
					MACAddress: "02:a2:b3:c4:d5:e6",
					Subnets:    []proto.NetAddress{{IPNet: netip.MustParsePrefix("1.2.3.4/32")}},
				},
			},
		})
	case "teardown":
		req := proto.NetworkPluginExec{}
		err := json.Unmarshal(msg.Raw, &req)
		if err != nil {
			replyError(conn, err.Error(), "")
			return
		}

		// Disconnect endpoint and clear it from our DB
		if err := netmgr.Down(req.ContainerName); err != nil {
			replyError(conn, err.Error(), req.ContainerName)
			return
		}
		reply(conn, struct{}{})
	case "info":
		reply(conn, proto.Info{
			Version:    network.Build,
			APIVersion: proto.APIVersion,
		})
	default:
		replyError(conn, "unknown command "+cmd, "")
	}
}

func replyError(conn net.Conn, msg string, container string) {
	if container == "" {
		container = "unknown"
	}
	slog.Error(msg, "container", container)
	reply(conn, proto.Error{Error: msg})
}

func reply(conn net.Conn, data any) {
	if err := json.NewEncoder(conn).Encode(data); err != nil {
		slog.Error("failed to reply to plugin", "error", err)
	}
}
