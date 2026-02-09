package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	homeDir, _ := os.UserHomeDir()
	rclone_path := filepath.Join(homeDir, ".config", "rclone", "rclone.conf")
	// 4. Get ID from server

	// rclone.conf에서 sessionID와 ssrfToken 가져오기
	rclone_conf_file, _ := os.Open(rclone_path)
	defer rclone_conf_file.Close()

	scanner := bufio.NewScanner(rclone_conf_file)
	var sessionID, ssrfToken string

	isUnic := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if !isUnic && strings.HasPrefix(line, "[unic]") {
			isUnic = true
		}

		if isUnic {
			// 키워드 포함 여부 확인
			if strings.HasPrefix(line, "sessionID") {
				sessionID = extractValue(line)
			} else if strings.HasPrefix(line, "ssrfToken") {
				ssrfToken = extractValue(line)
			}
		}
	}

	fmt.Println("Extracted SessionID:", sessionID)
	fmt.Println("Extracted SSRF Token:", ssrfToken)

	// request 객체 생성
	req, err := http.NewRequest("GET", "https://balneologic-pseudomiraculous-leonidas.ngrok-free.dev/api/v1", nil)
	if err != nil {
		// return nil, err
		return
	}

	req.Header.Set("X-XSRF-TOKEN", ssrfToken)
	req.Header.Set("JSESSIONID", sessionID)

	// HTTP request 전송
	resp, err := http.Get("https://jsonplaceholder.typicode.com/posts/1")
	if err != nil {
		// return nil, err
		return
	}
	defer resp.Body.Close()

	// HTTP response 확인
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(body))

	// HTTP response에서 쿠키 파싱해서

}

func extractValue(line string) string {
	parts := strings.Split(line, "=")
	if len(parts) < 2 {
		return ""
	}
	val := strings.TrimSpace(parts[1]) // {"value"}
	val = strings.TrimPrefix(val, "{\"")
	val = strings.TrimSuffix(val, "\"}")
	return val
}
