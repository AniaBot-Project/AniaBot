package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

// GitHub OAuth 设备授权流（Device Flow）在线登录。
//
// 无需回调地址，适合自托管面板：服务器向 GitHub 请求一次性设备码，
// 用户在浏览器打开 github.com/login/device 输入 8 位代码，服务器轮询
// 换取 access_token 并存入配置中心（与手动粘贴 Token 等效，仅提升限流配额）。
// 使用 AniaBot 官方 OAuth App 的固定 Client ID，开箱即用。
const (
	ghDeviceCodeURL  = "https://github.com/login/device/code"
	ghAccessTokenURL = "https://github.com/login/oauth/access_token"
	ghDeviceGrant    = "urn:ietf:params:oauth:grant-type:device_code"

	// GitHubOAuthClientID AniaBot 官方 GitHub OAuth App 的 Client ID。
	GitHubOAuthClientID = "Ov23li6fHYmQOGOmliT4"
)

// oauthFlow 一次设备授权流程的内存状态。
type oauthFlow struct {
	mu              sync.Mutex
	active          bool
	deviceCode      string
	userCode        string
	verificationURI string
	expiresAt       time.Time
	intervalSec     int
	status          string // pending / authorized / expired / failed
	user            string
	err             string
	cancel          context.CancelFunc
}

func newOAuthFlow() *oauthFlow { return &oauthFlow{} }

func (f *oauthFlow) setDone(status, user, err string) {
	f.mu.Lock()
	f.active = false
	f.status = status
	f.user = user
	f.err = err
	f.mu.Unlock()
}

func (f *oauthFlow) snapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{
		"active": f.active,
		"status": f.status,
		"user":   f.user,
		"error":  f.err,
	}
	if f.active {
		out["user_code"] = f.userCode
		out["verification_uri"] = f.verificationURI
		out["expires_at"] = f.expiresAt.Format(time.RFC3339)
		out["interval"] = f.intervalSec
	}
	return out
}

// oauthConfigured 在线登录是否可用（使用内置官方 OAuth App，固定可用）。
func (s *Service) oauthConfigured() bool {
	return true
}

// oauthUser 最近一次在线登录的 GitHub 用户名（用于面板展示）。
func (s *Service) oauthUser() string {
	return s.cfgStr("bot.marketplace.oauth_user")
}

// StartOAuth 开始一次设备授权流：向 GitHub 请求设备码并启动后台轮询。
func (s *Service) StartOAuth(ctx context.Context) (map[string]any, error) {
	clientID := GitHubOAuthClientID
	s.oauth.mu.Lock()
	if s.oauth.active {
		s.oauth.mu.Unlock()
		return nil, fmt.Errorf("已有进行中的 GitHub 登录流程，请先完成或等待其过期")
	}
	s.oauth.active = true
	s.oauth.status = "pending"
	s.oauth.user = ""
	s.oauth.err = ""
	s.oauth.mu.Unlock()

	resp, err := resty.New().R().SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetFormData(map[string]string{"client_id": clientID}).
		Post(ghDeviceCodeURL)
	if err != nil {
		s.oauth.setDone("failed", "", fmt.Sprintf("请求设备码失败: %v", err))
		return nil, err
	}
	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		ErrorDesc       string `json:"error_description"`
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		s.oauth.setDone("failed", "", "解析设备码响应失败")
		return nil, fmt.Errorf("解析设备码响应失败: %w", err)
	}
	if out.Error != "" {
		msg := out.Error
		if out.ErrorDesc != "" {
			msg += ": " + out.ErrorDesc
		}
		s.oauth.setDone("failed", "", msg)
		return nil, fmt.Errorf("GitHub 拒绝设备码请求: %s", msg)
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		s.oauth.setDone("failed", "", "设备码响应缺少必要字段")
		return nil, fmt.Errorf("设备码响应缺少必要字段")
	}
	interval := out.Interval
	if interval < 5 {
		interval = 5
	}
	expires := 900
	if out.ExpiresIn > 0 {
		expires = out.ExpiresIn
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	s.oauth.mu.Lock()
	s.oauth.deviceCode = out.DeviceCode
	s.oauth.userCode = out.UserCode
	s.oauth.verificationURI = out.VerificationURI
	if out.VerificationURI == "" {
		s.oauth.verificationURI = "https://github.com/login/device"
	}
	s.oauth.expiresAt = time.Now().Add(time.Duration(expires) * time.Second)
	s.oauth.intervalSec = interval
	s.oauth.cancel = cancel
	s.oauth.mu.Unlock()

	go s.pollOAuth(pollCtx, clientID, out.DeviceCode, interval)
	return map[string]any{
		"user_code":        out.UserCode,
		"verification_uri": s.oauth.verificationURI,
		"expires_in":       expires,
		"interval":         interval,
	}, nil
}

// OAuthStatus 返回当前设备授权流程状态（面板轮询）。
func (s *Service) OAuthStatus() map[string]any { return s.oauth.snapshot() }

// CancelOAuth 取消进行中的设备授权流程。
func (s *Service) CancelOAuth() {
	s.oauth.mu.Lock()
	cancel := s.oauth.cancel
	s.oauth.active = false
	s.oauth.status = "failed"
	s.oauth.err = "登录已取消"
	s.oauth.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// pollOAuth 按 GitHub 指定间隔轮询授权状态，成功后保存 Token 并记录用户名。
func (s *Service) pollOAuth(ctx context.Context, clientID, deviceCode string, intervalSec int) {
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			token, err := s.exchangeDeviceCode(ctx, clientID, deviceCode)
			if err != nil {
				msg := err.Error()
				switch {
				case strings.Contains(msg, "slow_down"):
					intervalSec += 5
					ticker.Reset(time.Duration(intervalSec) * time.Second)
					continue
				case strings.Contains(msg, "expired_token"):
					s.oauth.setDone("expired", "", "授权码已过期，请重新发起登录")
					return
				case strings.Contains(msg, "access_denied"):
					s.oauth.setDone("failed", "", "授权被拒绝")
					return
				default:
					// authorization_pending 等：继续轮询
					continue
				}
			}
			// 成功：保存 Token 并获取用户名
			user, _ := s.fetchUser(ctx, token)
			if err := s.SaveToken(token); err != nil {
				s.logger.Warn("保存 OAuth Token 失败", "error", err)
			}
			if user != "" {
				_ = s.cfg.Set("bot.marketplace.oauth_user", user)
			}
			s.oauth.setDone("authorized", user, "")
			s.logger.Info("GitHub 在线登录成功", "user", user)
			return
		}
	}
}

// exchangeDeviceCode 用设备码换取 access_token。
// 返回 "" 与 nil 之外的错误时，错误信息中包含 GitHub 的错误码（authorization_pending / slow_down 等）。
func (s *Service) exchangeDeviceCode(ctx context.Context, clientID, deviceCode string) (string, error) {
	resp, err := resty.New().R().SetContext(ctx).
		SetHeader("Accept", "application/json").
		SetFormData(map[string]string{
			"client_id":   clientID,
			"device_code": deviceCode,
			"grant_type":  ghDeviceGrant,
		}).
		Post(ghAccessTokenURL)
	if err != nil {
		return "", err
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		msg := out.Error
		if out.ErrorDesc != "" {
			msg += ": " + out.ErrorDesc
		}
		return "", fmt.Errorf("%s", msg)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("GitHub 未返回 access_token")
	}
	return out.AccessToken, nil
}

// fetchUser 用 Token 获取 GitHub 用户名（用于面板展示登录身份）。
func (s *Service) fetchUser(ctx context.Context, token string) (string, error) {
	resp, err := resty.New().R().SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+token).
		SetHeader("User-Agent", ghUserAgent).
		Get("https://api.github.com/user")
	if err != nil {
		return "", err
	}
	if resp.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("获取用户信息失败（HTTP %d）", resp.StatusCode())
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return "", err
	}
	return out.Login, nil
}
