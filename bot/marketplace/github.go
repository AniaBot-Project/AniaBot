package marketplace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/jeanhua/AniaBot/common/pluginmeta"
)

const (
	ghAPIBase     = "https://api.github.com"
	ghUserAgent   = "AniaBot-Plugin-Marketplace"
	defaultRepo   = "AniaBot-Project/AniaBot-Plugins"
	defaultBranch = "main"
)

// githubClient 插件市场的 GitHub API 客户端（可带 Token 提升限流配额）。
type githubClient struct {
	owner  string
	repo   string
	token  string
	client *resty.Client
	// rateRemaining 最近一次 API 响应的剩余配额（-1 表示未知）。
	rateRemaining int
}

// parseRepo 解析 owner/repo 或完整 URL，非法时回退默认仓库。
func parseRepo(repo string) (owner, name string) {
	r := strings.TrimSpace(repo)
	r = strings.TrimSuffix(r, "/")
	r = strings.TrimPrefix(r, "https://github.com/")
	r = strings.TrimPrefix(r, "http://github.com/")
	parts := strings.Split(r, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	dp := strings.Split(defaultRepo, "/")
	return dp[0], dp[1]
}

func newGitHubClient(repo, token string) *githubClient {
	owner, name := parseRepo(repo)
	c := &githubClient{owner: owner, repo: name, token: strings.TrimSpace(token), rateRemaining: -1}
	c.client = resty.New().
		SetBaseURL(ghAPIBase).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("X-GitHub-Api-Version", "2022-11-28").
		SetHeader("User-Agent", ghUserAgent)
	if c.token != "" {
		c.client.SetAuthToken(c.token)
	}
	return c
}

// ghContent GitHub contents API 响应。
type ghContent struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// apiErr 记录 GitHub API 调用失败。
type apiErr struct {
	Status int
	Body   string
}

func (e *apiErr) Error() string {
	return fmt.Sprintf("GitHub API 请求失败（HTTP %d）: %s", e.Status, e.Body)
}

// isRateLimit 判断是否触发 GitHub 限流（403/429 + rate limit 关键字或配额为 0）。
func isRateLimit(status int, body, rateHeader string) bool {
	if rateHeader == "0" {
		return true
	}
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		low := strings.ToLower(body)
		return strings.Contains(low, "rate limit") || strings.Contains(low, "rate_limit")
	}
	return false
}

// latestCommit 返回分支最新 commit SHA。
func (c *githubClient) latestCommit(ctx context.Context, branch string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	if err := c.do(ctx, "GET", "/repos/"+c.owner+"/"+c.repo+"/commits/"+branch, nil, &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", fmt.Errorf("GitHub 未返回 commit SHA（分支 %s）", branch)
	}
	return out.SHA, nil
}

// fetchContent 读取仓库内单个文本/JSON 文件（contents API，base64）。
func (c *githubClient) fetchContent(ctx context.Context, path, ref string) ([]byte, error) {
	var out ghContent
	url := "/repos/" + c.owner + "/" + c.repo + "/contents/" + path
	if ref != "" {
		url += "?ref=" + ref
	}
	if err := c.do(ctx, "GET", url, nil, &out); err != nil {
		return nil, err
	}
	if out.Type != "file" {
		return nil, fmt.Errorf("路径 %s 不是文件（type=%s）", path, out.Type)
	}
	if out.Encoding == "base64" {
		data, err := base64.StdEncoding.DecodeString(out.Content)
		if err != nil {
			return nil, fmt.Errorf("解码 %s 失败: %w", path, err)
		}
		return data, nil
	}
	return []byte(out.Content), nil
}

// fetchIndex 读取并校验聚合索引 index.json。
func (c *githubClient) fetchIndex(ctx context.Context, ref string) (*pluginmeta.Index, error) {
	data, err := c.fetchContent(ctx, "index.json", ref)
	if err != nil {
		return nil, err
	}
	var idx pluginmeta.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("解析 index.json 失败: %w", err)
	}
	for i := range idx.Plugins {
		if err := idx.Plugins[i].Validate(); err != nil {
			return nil, err
		}
	}
	return &idx, nil
}

// fetchReadme 读取插件 README：文件不存在（404）时返回空串，不视为错误；
// 其余失败（Token 失效/限流/网络）原样返回，避免被误判为「未提供 README」。
func (c *githubClient) fetchReadme(ctx context.Context, id, readmeName, ref string) (string, error) {
	data, err := c.fetchContent(ctx, "plugins/"+id+"/"+readmeName, ref)
	if err != nil {
		var ae *apiErr
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// verifyToken 校验已保存的 GitHub Token 是否仍有效，返回真实登录用户名。
// ok=true 表示 Token 有效；invalid=true 表示 Token 已失效（401）；
// 两者都为 false 表示网络/其他异常无法确认（不应据此误报「登录已失效」）。
func (c *githubClient) verifyToken(ctx context.Context) (user string, ok bool, invalid bool) {
	if c.token == "" {
		return "", false, false
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, "GET", "/user", nil, &out); err != nil {
		var ae *apiErr
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			return "", false, true
		}
		return "", false, false
	}
	return out.Login, true, false
}

// downloadPlugin 下载仓库 tar 包并只解压 plugins/<id>/ 到 dstDir。
func (c *githubClient) downloadPlugin(ctx context.Context, ref, id, dstDir string) error {
	url := "/repos/" + c.owner + "/" + c.repo + "/tarball/" + ref
	resp, err := c.client.R().SetContext(ctx).Get(url)
	if err != nil {
		return fmt.Errorf("下载插件源码失败: %w", err)
	}
	c.observeRate(resp)
	if resp.StatusCode() != http.StatusOK {
		if isRateLimit(resp.StatusCode(), string(resp.Body()), resp.Header().Get("X-RateLimit-Remaining")) {
			return fmt.Errorf("GitHub API 限流，请稍后再试或在面板配置 GitHub Token")
		}
		return fmt.Errorf("下载插件源码失败（HTTP %d）", resp.StatusCode())
	}
	prefix := "plugins/" + id + "/"
	gr, err := gzip.NewReader(bytes.NewReader(resp.Body()))
	if err != nil {
		return fmt.Errorf("解压插件源码失败: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取插件源码压缩包失败: %w", err)
		}
		// 去掉归档根目录（<repo>-<sha>/），只保留 plugins/<id>/ 下的文件
		parts := strings.SplitN(filepath.ToSlash(hdr.Name), "/", 2)
		if len(parts) < 2 {
			continue
		}
		rel := parts[1]
		if !strings.HasPrefix(rel, prefix) {
			continue
		}
		out := filepath.Join(dstDir, strings.TrimPrefix(rel, prefix))
		if !isWithin(dstDir, out) {
			return fmt.Errorf("插件压缩包包含非法路径: %s", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // 忽略符号链接/设备等，防止逃逸
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}

// observeRate 记录限流配额（仅记录 header 值）。
func (c *githubClient) observeRate(resp *resty.Response) {
	if v := resp.Header().Get("X-RateLimit-Remaining"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			c.rateRemaining = n
		}
	}
}

// do 执行一次 API 请求并解析 JSON 响应。
func (c *githubClient) do(ctx context.Context, method, url string, body, out any) error {
	req := c.client.R().SetContext(ctx)
	if body != nil {
		req.SetBody(body)
	}
	resp, err := req.Execute(method, url)
	if err != nil {
		return fmt.Errorf("GitHub API 请求失败: %w", err)
	}
	c.observeRate(resp)
	if resp.StatusCode() != http.StatusOK {
		msg := string(resp.Body())
		if isRateLimit(resp.StatusCode(), msg, resp.Header().Get("X-RateLimit-Remaining")) {
			return fmt.Errorf("GitHub API 限流，请稍后再试或在面板配置 GitHub Token")
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &apiErr{Status: resp.StatusCode(), Body: msg}
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body(), out); err != nil {
			return fmt.Errorf("解析 GitHub API 响应失败: %w", err)
		}
	}
	return nil
}

// isWithin 判断 child 是否位于 parent 目录内（防路径穿越）。
func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
