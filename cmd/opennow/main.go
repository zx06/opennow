package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/zx06/opennow/internal/cli"
	"github.com/zx06/opennow/internal/config"
	embedadmin "github.com/zx06/opennow/internal/embed"
	"github.com/zx06/opennow/internal/server"
	"github.com/zx06/opennow/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "publish":
		runPublish(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "delete":
		runDelete(os.Args[2:])
	case "rollback":
		runRollback(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `opennow — 自托管静态站点发布

用法:
  opennow serve [flags]
  opennow publish [--slug SLUG] [--server URL] <path>
  opennow list [--server URL]
  opennow delete <slug> [--server URL]
  opennow rollback <slug> <version-id> [--server URL]

环境变量:
  OPENNOW_API_KEY      管理 API 密钥
  OPENNOW_DATA_DIR     本地数据目录（默认 ./data）
  OPENNOW_ADDR         监听地址（默认 :8080）
  OPENNOW_BASE_DOMAIN  子域名模式后缀（如 xuzhuo.xyz）
  OPENNOW_PUBLIC_URL   路径模式公开 URL 前缀
  OPENNOW_SECRET       Cookie/预览令牌签名密钥（默认自动生成 <DATA_DIR>/secret.key）
  OPENNOW_STORAGE      存储后端：local（默认）| s3 | r2 | github
                       s3: OPENNOW_S3_ENDPOINT/REGION/BUCKET/PREFIX/ACCESS_KEY_ID/SECRET_ACCESS_KEY/PATH_STYLE
                       r2: OPENNOW_R2_ACCOUNT_ID/BUCKET/ACCESS_KEY_ID/SECRET_ACCESS_KEY/PREFIX
                       github: OPENNOW_GITHUB_TOKEN/REPO(owner/repo)/BRANCH/PREFIX
                       详见 README

`)
}

func runServe(args []string) {
	flagSet := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flagSet.String("addr", "", "listen address")
	dataDir := flagSet.String("data-dir", "", "storage data directory")
	_ = flagSet.Parse(args)

	cfg := config.FromEnv()
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if err := cfg.EnsureSecret(); err != nil {
		log.Fatalf("初始化签名密钥失败: %v", err)
	}
	store, err := storage.Open(cfg.Backend, cfg.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	srv := server.New(cfg, store, embedadmin.AdminHandler())
	log.Printf("opennow listening on %s (admin %s)", cfg.Addr, cfg.AdminPrefix)
	log.Fatal(http.ListenAndServe(cfg.Addr, srv.Handler()))
}

func apiClientFlags(args []string) (*cli.Client, []string) {
	fs := flag.NewFlagSet("cmd", flag.ExitOnError)
	serverURL := fs.String("server", env("OPENNOW_SERVER", "http://127.0.0.1:8080"), "opennow server URL")
	_ = fs.Parse(args)
	key := os.Getenv("OPENNOW_API_KEY")
	if key == "" {
		log.Fatal("需要 OPENNOW_API_KEY")
	}
	return cli.NewClient(*serverURL, key), fs.Args()
}

func runPublish(args []string) {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	serverURL := fs.String("server", env("OPENNOW_SERVER", "http://127.0.0.1:8080"), "opennow server URL")
	slug := fs.String("slug", "", "site slug")
	_ = fs.Parse(args)
	if len(fs.Args()) != 1 {
		log.Fatal("用法: opennow publish <path>")
	}
	key := os.Getenv("OPENNOW_API_KEY")
	if key == "" {
		log.Fatal("需要 OPENNOW_API_KEY")
	}
	out, err := cli.NewClient(*serverURL, key).Publish(*slug, fs.Args()[0])
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func runList(args []string) {
	c, rest := apiClientFlags(args)
	if len(rest) != 0 {
		log.Fatal("用法: opennow list")
	}
	sites, err := c.ListSites()
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sites)
}

func runDelete(args []string) {
	c, rest := apiClientFlags(args)
	if len(rest) != 1 {
		log.Fatal("用法: opennow delete <slug>")
	}
	if err := c.DeleteSite(rest[0]); err != nil {
		log.Fatal(err)
	}
	fmt.Println("deleted")
}

func runRollback(args []string) {
	c, rest := apiClientFlags(args)
	if len(rest) != 2 {
		log.Fatal("用法: opennow rollback <slug> <version-id>")
	}
	if err := c.Rollback(rest[0], rest[1]); err != nil {
		log.Fatal(err)
	}
	fmt.Println("rolled back")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
