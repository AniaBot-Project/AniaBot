package pluginaichat

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif" // 注册 GIF 解码器（转码备用）
	"image/jpeg"  // JPEG 解码注册 + baseline 重编码输出

	"image/png" // PNG 解码注册 + 转码输出
	"io"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	_ "golang.org/x/image/bmp" // 注册 BMP 解码器：QQ 截图/表情常见 BMP，需转码后才被多模态模型接受
)

const (
	// imageFetchTimeout 单张图片下载超时。
	imageFetchTimeout = 30 * time.Second
	// imageFetchMaxBytes 图片大小上限：超过则拒绝加载，避免超大 base64 撑爆上下文。
	imageFetchMaxBytes = 25 << 20
	// imageFetchUserAgent 下载 QQ 等平台图片链接时使用的 UA，避免 CDN 因默认 UA 拒绝。
	imageFetchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
)

// fetchImageDataURI 把图片引用统一为 data URI：
//   - data: URI 解码出原始字节后按内容重新规范化（适配器贴错 MIME 标签、
//     渐进式 JPEG 等问题在此统一修正，不再原样透传）；
//   - http(s) URL 在本机下载后走同一规范化流程。
//
// 这样上游模型服务不再需要自己拉取 QQ 临时链接（rkey 过期、机房拉不到时
// 会报"不支持的图片"400 错误），且格式始终在模型服务支持范围内。
func fetchImageDataURI(ctx context.Context, ref string) (string, error) {
	data := []byte(nil)
	switch {
	case strings.HasPrefix(ref, "data:"):
		payload, err := decodeDataURIPayload(ref)
		if err != nil {
			return "", err
		}
		data = payload
	case strings.HasPrefix(ref, "base64://"):
		payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ref, "base64://"))
		if err != nil {
			return "", fmt.Errorf("base64 图片解码失败: %w", err)
		}
		data = payload
	default:
		client := resty.New().
			SetTimeout(imageFetchTimeout).
			SetHeader("User-Agent", imageFetchUserAgent).
			// 不自动解析响应体：先按上限流式读取，避免恶意/异常 URL 在体积检查
			//（下方的 imageFetchMaxBytes）之前就把数百 MB 读进内存造成 OOM
			SetDoNotParseResponse(true)
		resp, err := client.R().SetContext(ctx).Get(ref)
		if err != nil {
			return "", fmt.Errorf("下载图片失败: %w", err)
		}
		body := resp.RawBody()
		if body == nil {
			return "", fmt.Errorf("下载图片失败: 响应体为空")
		}
		defer body.Close()
		if !resp.IsSuccess() {
			return "", fmt.Errorf("下载图片失败: HTTP %d", resp.StatusCode())
		}
		// 多读 1 字节以区分"恰好等于上限"与"超过上限"
		limited, err := io.ReadAll(io.LimitReader(body, imageFetchMaxBytes+1))
		if err != nil {
			return "", fmt.Errorf("读取图片失败: %w", err)
		}
		if int64(len(limited)) > imageFetchMaxBytes {
			return "", fmt.Errorf("图片过大（超过上限 %.1fMB）", float64(imageFetchMaxBytes)/(1<<20))
		}
		data = limited
	}
	if len(data) == 0 {
		return "", fmt.Errorf("图片内容为空")
	}
	if len(data) > imageFetchMaxBytes {
		return "", fmt.Errorf("图片过大（%.1fMB，上限 %.1fMB）", float64(len(data))/(1<<20), float64(imageFetchMaxBytes)/(1<<20))
	}
	return imageDataURI(data)
}

// imageDataURI 把图片字节规范化为可直接发给多模态模型的 data URI：
//   - 按魔数识别真实格式，jpeg 统一重编码为 baseline（渐进式/CMYK 等 JPEG
//     即使 MIME 标签正确，部分模型服务解码也会报"unsupported image"）；
//   - webp/png/gif 直接内联原始字节；
//   - 其余格式（如 QQ 常见的 BMP）解码后转码为 PNG。
func imageDataURI(data []byte) (string, error) {
	if mime, ok := sniffImageMIME(data); ok {
		if mime == "image/jpeg" {
			buf, err := reencodeBaselineJPEG(data)
			if err != nil {
				// 本机解码都失败的 JPEG（截断/损坏/罕见变体）原样转发只会让
				// 模型服务整轮 400，这里按单图加载失败处理
				return "", fmt.Errorf("JPEG 图片解码失败: %v", err)
			}
			data = buf
		}
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	// 不支持直接内联的格式（如 BMP）：解码后转码为 PNG
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("无法识别的图片格式: %v", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("图片转码失败: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// decodeDataURIPayload 解码 data:[<mime>];base64,<payload> 的 base64 负载，
// 忽略其中声明的 MIME（可能被适配器贴错，格式以实际字节为准）。
func decodeDataURIPayload(uri string) ([]byte, error) {
	_, payload, ok := strings.Cut(uri, ",")
	if !ok {
		return nil, fmt.Errorf("data URI 格式无效（缺少逗号分隔符）")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("data URI base64 解码失败: %w", err)
	}
	return data, nil
}

// reencodeBaselineJPEG 解码任意 JPEG 变体（渐进式/CMYK 等）并重编码为
// 标准 baseline JPEG。
func reencodeBaselineJPEG(data []byte) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sniffImageMIME 按文件魔数识别模型服务可直接接受的图片格式，返回 MIME 与是否识别成功。
func sniffImageMIME(data []byte) (string, bool) {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg", true
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png", true
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", true
	default:
		return "", false
	}
}
