package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/gomarkdown/markdown"
	"github.com/gorilla/mux"
	"github.com/jessevdk/go-flags"
)

var Options struct {
	Listen  int           `short:"p" long:"listen" env:"LISTEN_PORT" required:"true" description:"Listen is started on this port."`
	Timeout time.Duration `long:"timeout" env:"TIMEOUT" description:"Duration for which the server gracefully wait for existing connections to finish - e.g. 15s or 1m"`
	Verbose int           `short:"v" long:"verbose" env:"VERBOSE" description:"Be verbose."`
}

// Owner of the repository
type Owner struct {
	Login string
}

// Item is the single repository data structure
type Item struct {
	ID    int
	Name  string
	Owner Owner
}

// JSONData contains the GitHub API response
type JSONData struct {
	Count int `json:"total_count"`
	Items []Item
}

func main() {
	ParseArgs(&Options)

	glog.Infof("Start server on :%d", Options.Listen)
	r := mux.NewRouter()
	r.HandleFunc("/", handler)
	r.HandleFunc("/health", livenessHandler)
	http.Handle("/", r)

	srv := &http.Server{
		Handler:      r,
		Addr:         fmt.Sprintf(":%d", Options.Listen),
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		srv.ListenAndServe()
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	<-sigs
	ctx, cancel := context.WithTimeout(context.Background(), Options.Timeout)
	defer cancel()
	srv.Shutdown(ctx)
	glog.Info("Signal received: now exiting")
	os.Exit(0)
}

func handler(w http.ResponseWriter, r *http.Request) {
	res, err := http.Get("https://api.github.com/search/repositories?q=org:dcos-terraform&order=desc&per_page=200")
	CheckErrorFatal(err)
	body, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	CheckErrorFatal(err)
	if res.StatusCode != http.StatusOK {
		log.Fatal("Unexpected status code", res.StatusCode)
	}
	data := JSONData{}
	err = json.Unmarshal(body, &data)
	CheckErrorFatal(err)

	w.Header().Set("Cache-Control", "max-age=600")
	fmt.Fprint(w, renderMarkdownHtml(data))
}

func renderMarkdownHtml(data JSONData) string {
	if glog.V(5) {
		glog.Infof("Repositories overall: %d", data.Count)
	}
	topic := []byte("# DC/OS Terraform modules - CI STATUS\n")
	tablehead := []byte("| Repository | master | support/0.2.x | support/0.1.x |\n")
	tablesplit := []byte("| --- | --- | --- | --- |\n")
	md := append(topic, tablehead...)
	md = append(md, tablesplit...)
	status_badge_icon_prefix := "[![Build Status](https://jenkins-terraform.mesosphere.com/service/dcos-terraform-jenkins/buildStatus/icon?job=dcos-terraform%2F"
	status_badge_link_prefix := "(https://jenkins-terraform.mesosphere.com/service/dcos-terraform-jenkins/job/dcos-terraform/job/"

	for _, i := range data.Items {
		md = append(md, "| "+i.Name+" | "+status_badge_icon_prefix+i.Name+"%2Fmaster)]"+status_badge_link_prefix+i.Name+"/job/master/) | "+status_badge_icon_prefix+i.Name+"%2Fsupport%252F0.2.x)]"+status_badge_link_prefix+i.Name+"/job/support%252F0.2.x/) | "+status_badge_icon_prefix+i.Name+"%2Fsupport%252F0.1.x)]"+status_badge_link_prefix+i.Name+"/job/support%252F0.1.x/) |\n"...)
	}
	return string(markdown.ToHTML(md, nil, nil))
}

func livenessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// ParseArgs needs a struct compatible to jeddevdk/go-flags and will fill it
// based on CLI parameters.
func ParseArgs(options interface{}) {
	_, err := flags.ParseArgs(options, os.Args)
	if err != nil {
		if err.(*flags.Error).Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			panic(err)
		}
	}

	fixGlog(options)
}

// ErrorPrintHelpAndExit prints the message, the help message and exits
func ErrorPrintHelpAndExit(options interface{}, message string) {
	fmt.Fprintln(os.Stderr, message+"\n")
	var parser = flags.NewParser(options, flags.Default)
	parser.WriteHelp(os.Stderr)
	os.Exit(1)
}

// configure glog, not used for flag parsing
func fixGlog(options interface{}) {
	flag.Set("logtostderr", "true")

	verbose := reflect.ValueOf(options).Elem().FieldByName("Verbose")
	if verbose.IsValid() {
		flag.Set("v", strconv.Itoa(verbose.Interface().(int)))
	}
	flag.CommandLine.Parse([]string{})
}

// CheckErrorFatal to glog.Fatalf
func CheckErrorFatal(err error) {
	if err != nil {
		glog.Fatalf("An error occurred. %v", err)
	}
}
