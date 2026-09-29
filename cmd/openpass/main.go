package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"openpass/internal/api"
	openruntime "openpass/internal/runtime"
	"openpass/internal/store"
)

var version = "0.1.9"

func main() {
	listen := flag.String("listen", ":8787", "HTTP listen address")
	statePath := flag.String("state", "/etc/openpass/state.json", "persistent state file")
	webDir := flag.String("web", "/usr/share/openpass/web", "directory containing the web UI")
	configPath := flag.String("config", "/var/etc/openpass/sing-box.json", "generated sing-box configuration")
	singBoxPath := flag.String("sing-box", "/usr/bin/sing-box", "sing-box executable")
	nftPath := flag.String("nft", "/var/run/openpass/91-openpass-dynamic.nft", "dynamic nftables policy file")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*statePath), 0755); err != nil {
		log.Fatal(err)
	}
	st, err := store.New(*statePath)
	if err != nil {
		log.Fatal(err)
	}
	server := api.New(st)
	server.ConfigPath = *configPath
	server.SingBoxPath = *singBoxPath
	server.Version = version
	rt := openruntime.New(*singBoxPath, *configPath)
	rt.NFTPath = *nftPath
	server.Runtime = rt
	if st.Settings().Enabled {
		if err := rt.Apply(st.State()); err != nil {
			log.Printf("restore sing-box: %v", err)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", server.Handler())
	// In development the web directory may not exist yet. Keep API available
	// and return a useful response instead of failing process startup.
	if info, e := os.Stat(*webDir); e == nil && info.IsDir() {
		files := http.FileServer(http.Dir(*webDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			if r.URL.Path != "/" && !strings.Contains(filepath.Base(r.URL.Path), ".") {
				http.ServeFile(w, r, filepath.Join(*webDir, "index.html"))
				return
			}
			files.ServeHTTP(w, r)
		})
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "OpenPass web assets are not installed", http.StatusNotFound)
		})
	}
	log.Printf("OpenPass %s listening on %s", version, *listen)
	httpServer := &http.Server{Addr: *listen, Handler: mux}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-stop
	rt.Stop()
	_ = httpServer.Close()
}
