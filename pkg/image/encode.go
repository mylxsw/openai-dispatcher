package image

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// Base64ImageMediaType 获取 base64 图片的 MIME 类型
func Base64ImageMediaType(base64Image string) (string, error) {
	_, mimeType, err := DecodeBase64ImageWithMime(base64Image)
	if err != nil {
		return "", err
	}

	return mimeType, nil
}

// DecodeBase64ImageWithMime 解码 base64 图片
func DecodeBase64ImageWithMime(base64Image string) (data []byte, mimeType string, err error) {
	// Remove data:image/jpeg;base64, if exist
	d := strings.SplitN(base64Image, ",", 2)
	if len(d) == 2 {
		base64Image = d[1]
	}

	// Decode the base64 image
	decodedData, err := base64.StdEncoding.DecodeString(base64Image)
	if err != nil {
		return nil, "", err
	}

	return decodedData, http.DetectContentType(decodedData), nil
}

// RemoveImageBase64Prefix 移除 base64 图片的前缀
func RemoveImageBase64Prefix(base64Image string) string {
	return strings.SplitN(base64Image, ",", 2)[1]
}
