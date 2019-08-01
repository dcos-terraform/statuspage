package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/google/go-github/v27/github"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/jessevdk/go-flags"
	"golang.org/x/oauth2"
)

var Options struct {
	Listen            int           `short:"p" long:"listen" env:"LISTEN_PORT" required:"true" description:"Listen is started on this port."`
	GitHubAccessToken string        `short:"t" long:"ghatoken" env:"GITHUB_ACCESS_TOKEN" required:"true" description:"Token for identifing the application."`
	GitHubOrg         string        `short:"o" long:"ghorg" env:"GITHUB_ORG" required:"true" description:"GitHub Org being fetched for Repositories."`
	GitHubRepoPrefix  string        `long:"ghreporefresh" default:"terraform-" env:"GITHUB_REPO_PREFIX" required:"false" description:"GitHub repo prefix."`
	GitHubOrgRefresh  time.Duration `long:"ghorgrefresh" default:"60m" env:"GITHUB_ORG_REFRESH" required:"false" description:"Time the GitHub Org being fetched repos from."`
	CiStatusRefresh   time.Duration `long:"cistatusrefresh" default:"3m" env:"CI_STATUS_REFRESH" required:"false" description:"Time the CI status is being fetched."`
	Timeout           time.Duration `long:"timeout" env:"TIMEOUT" description:"Duration for which the server gracefully wait for existing connections to finish - e.g. 15s or 1m"`
	Verbose           int           `short:"v" long:"verbose" env:"VERBOSE" description:"Be verbose."`
}

const (
	STATIC_DIR      = "/static/"
	STATIC_CSS_FILE = "bootstrap.min.css"
	GENERATOR       = `  <meta name="GENERATOR" content="dcos-terraform-statuspage`
	HEAD_EXTRA      = `  <link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png">
  <link rel="icon" type="image/png" sizes="32x32" href="/favicon-32x32.png">
  <link rel="icon" type="image/png" sizes="16x16" href="/favicon-16x16.png">
  <link rel="manifest" href="/site.webmanifest">
  <link rel="mask-icon" href="/safari-pinned-tab.svg" color="#5bbad5">
  <meta name="msapplication-TileColor" content="#da532c">
  <meta name="theme-color" content="#ffffff">`
)

type Badge struct {
	Result int
	Image  string
}

type CiResult struct {
	BranchesIndex           int
	BranchHtmlDoubleEncoded string
	Build                   *Badge
}

var markdownCache []byte
var provider []string
var branches []string
var repos map[string][]*github.Repository
var ciStatus []CiResult

func main() {
	ParseArgs(&Options)
	provider = append(provider, []string{"aws", "azurerm", "gcp", "null", "template"}...)
	branches = append(branches, []string{"support/0.2.x", "support/0.1.x"}...)
	repos = make(map[string][]*github.Repository, len(provider))

	r := mux.NewRouter()
	r.HandleFunc("/", handler)
	r.HandleFunc("/health", livenessHandler)

	files, err := ioutil.ReadDir(STATIC_DIR + "images/favicon")
	CheckErrorFatal(err)
	for _, file := range files {
		r.HandleFunc("/"+file.Name(), faviconHandler)
	}
	r.PathPrefix(STATIC_DIR).Handler(http.StripPrefix(STATIC_DIR, http.FileServer(http.Dir(STATIC_DIR))))
	http.Handle("/", r)

	walkErr := r.Walk(func(route *mux.Route, router *mux.Router, ancestors []*mux.Route) error {
		pathTemplate, _ := route.GetPathTemplate()
		glog.Infof("Registered: %s", pathTemplate)
		return nil
	})
	CheckErrorFatal(walkErr)

	srv := &http.Server{
		Handler:      handlers.ProxyHeaders(r),
		Addr:         fmt.Sprintf(":%d", Options.Listen),
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	done := make(chan bool)
	go func() {
		fetchRepositorys(Options.GitHubOrg)
		markdownContent()
		done <- true
		for {
			<-time.After(Options.GitHubOrgRefresh)
			go fetchRepositorys(Options.GitHubOrg)
		}
	}()
	go func() {
		for {
			<-time.After(Options.CiStatusRefresh)
			go markdownContent()
		}
	}()

	if glog.V(9) {
		glog.Infof("Waiting for initial fetchRepositorys(\"%s\") and markdownContent() to be done", Options.GitHubOrg)
	}

	<-done

	glog.Infof("Start server on :%d", Options.Listen)
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

func fetchRepositorys(org string) []*github.Repository {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: Options.GitHubAccessToken},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	opt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{PerPage: 10},
	}

	var allRepos []*github.Repository
	for {
		repos, resp, err := client.Repositories.ListByOrg(ctx, org, opt)
		CheckErrorFatal(err)
		allRepos = append(allRepos, repos...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	for _, i := range provider {
		repos[i] = nil
		for _, repo := range allRepos {
			// Non archived repos only
			if *repo.Archived != true {
				// Only repos matching our current module patterns
				r, _ := regexp.Compile("^(" + Options.GitHubRepoPrefix + ")(" + i + ").*$")
				if r.MatchString(*repo.Name) {
					repos[i] = append(repos[i], repo)
				}
			}
		}
	}

	return nil
}

func getJenkinsBuildStatusBadge(repoName string) []CiResult {
	done := make(chan bool)
	returnCiRes := make([]CiResult, 0)
	if glog.V(9) {
		glog.Infof("Repo to check: %s", repoName)
	}
	sliceSize := len(branches)
	for i, branch := range branches {
		go func(i int, b string) {
			branchHtmlDoubleEncoded := url.QueryEscape(url.QueryEscape(b))
			res, err := http.Get("https://jenkins-terraform.mesosphere.com/service/dcos-terraform-jenkins/buildStatus/text?job=dcos-terraform%2F" + repoName + "%2F" + branchHtmlDoubleEncoded)
			CheckErrorFatal(err)
			body, err := ioutil.ReadAll(res.Body)
			res.Body.Close()
			CheckErrorFatal(err)
			if glog.V(9) {
				glog.Infof("Result jenkins request for \"%s\" in branch \"%s\": %s", repoName, b, string(body))
			}

			cires := new(CiResult)
			badge := new(Badge)
			if res.StatusCode != http.StatusOK {
				badge.Image = STATIC_DIR + "images/0-build-notrun.svg"
				badge.Result = 0
			}

			switch true {
			case string(body) == "Success":
				badge.Image = STATIC_DIR + "images/1-build-passing.svg"
				badge.Result = 1
			case string(body) == "In progress":
				badge.Image = STATIC_DIR + "images/2-build-running.svg"
				badge.Result = 2
			case string(body) == "Failed":
				badge.Image = STATIC_DIR + "images/3-build-failing.svg"
				badge.Result = 3
			case string(body) == "Aborted":
				badge.Image = STATIC_DIR + "images/4-build-aborted.svg"
				badge.Result = 4
			default:
				badge.Image = STATIC_DIR + "images/0-build-notrun.svg"
				badge.Result = 0
			}

			cires.BranchesIndex = i
			cires.BranchHtmlDoubleEncoded = branchHtmlDoubleEncoded
			cires.Build = badge
			returnCiRes = append(returnCiRes, *cires)
			if len(returnCiRes) == sliceSize {
				done <- true
			}
		}(i, branch)
	}

	<-done
	return returnCiRes
}

func markdownContent() []byte {
	if glog.V(5) {
		for _, p := range provider {
			glog.Infof("Repositories "+p+": %d", len(repos[p]))
		}
	}

	var md []byte
	separator := []byte("---\n")
	topic := []byte("# DC/OS Terraform modules\n")
	md = append(md, topic...)

	for _, p := range provider {
		md = append(md, separator...)
		providers := []byte("### Provider: **" + p + "**\n")
		tablehead := []byte("| Repository | support/0.2.x | support/0.1.x |\n")
		tablesplit := []byte("| --- | --- | --- |\n")
		md = append(md, providers...)
		md = append(md, tablehead...)
		md = append(md, tablesplit...)

		status_badge_icon_prefix := "[![Build Status]("
		status_badge_link_prefix := "(https://jenkins-terraform.mesosphere.com/service/dcos-terraform-jenkins/job/dcos-terraform/job/"

		for _, repo := range repos[p] {
			md = append(md, "| "+*repo.Name+" | "+status_badge_icon_prefix...)

			badges := getJenkinsBuildStatusBadge(*repo.Name)
			// sort
			sort.SliceStable(badges, func(i, j int) bool {
				return badges[i].BranchesIndex < badges[j].BranchesIndex
			})

			lastBadge := len(badges) - 1
			for i, badge := range badges {
				if glog.V(9) {
					glog.Infof("Branch \"%s\" gets \"%s\"", branches[badge.BranchesIndex], badge.Build.Image)
				}
				if i == lastBadge {
					md = append(md, badge.Build.Image+")]"+status_badge_link_prefix+*repo.Name+"/job/"+badge.BranchHtmlDoubleEncoded+"/) "...)
				} else {
					md = append(md, badge.Build.Image+")]"+status_badge_link_prefix+*repo.Name+"/job/"+badge.BranchHtmlDoubleEncoded+"/) | "+status_badge_icon_prefix...)
				}
			}
			md = append(md, "|\n"...)
		}
	}
	markdownCache = md
	return nil
}

func renderMarkdownHtml() string {
	flags := html.CommonFlags | html.CompletePage | html.HrefTargetBlank
	opts := html.RendererOptions{
		Title:     "DC/OS Terraform modules",
		Flags:     flags,
		CSS:       STATIC_DIR + "css/" + STATIC_CSS_FILE,
		Icon:      "/favicon.ico",
		Head:      []byte(HEAD_EXTRA),
		Generator: GENERATOR,
	}
	renderer := html.NewRenderer(opts)
	return string(markdown.ToHTML(markdownCache, nil, renderer))
}

func handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "max-age=600")
	fmt.Fprint(w, renderMarkdownHtml())
}

func livenessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func faviconHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, STATIC_DIR+"images/favicon"+r.URL.Path)
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
