package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// autoLoginSpec 从环境变量读取自动登录配置,返回 nil 表示未启用。
//
// PGGO_DSN 优先;否则使用 PGGO_HOST / PGGO_PORT / PGGO_USER / PGGO_PASSWORD /
// PGGO_DB / PGGO_SSLMODE / PGGO_SOCKET / PGGO_AUTH。
// 证书认证额外读取 PGGO_SSLROOT / PGGO_SSLCERT / PGGO_SSLKEY 指向的文件。
func autoLoginSpec() (*ConnSpec, error) {
	if dsn := strings.TrimSpace(os.Getenv("PGGO_DSN")); dsn != "" {
		return &ConnSpec{Method: authDSN, DSN: dsn}, nil
	}

	user := strings.TrimSpace(os.Getenv("PGGO_USER"))
	if user == "" {
		return nil, nil
	}

	method := strings.TrimSpace(os.Getenv("PGGO_AUTH"))
	if method == "" {
		method = authPassword
	}
	switch method {
	case authPassword, authTrust, authCert:
	default:
		return nil, fmt.Errorf("PGGO_AUTH 仅支持 password / trust / cert,当前为 %q", method)
	}

	port := 5432
	if v := strings.TrimSpace(os.Getenv("PGGO_PORT")); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("PGGO_PORT 无效: %q", v)
		}
		port = p
	}

	host := strings.TrimSpace(os.Getenv("PGGO_HOST"))
	if host == "" {
		host = "127.0.0.1"
	}
	db := strings.TrimSpace(os.Getenv("PGGO_DB"))
	if db == "" {
		db = "postgres"
	}
	ssl := strings.TrimSpace(os.Getenv("PGGO_SSLMODE"))
	if ssl == "" {
		ssl = "disable"
	}

	spec := &ConnSpec{
		Method:   method,
		Host:     host,
		Port:     port,
		User:     user,
		Password: os.Getenv("PGGO_PASSWORD"),
		Database: db,
		SSLMode:  ssl,
		Socket:   truthy(os.Getenv("PGGO_SOCKET")),
	}
	if method == authCert {
		var err error
		if spec.CAPEM, err = readEnvFile("PGGO_SSLROOT"); err != nil {
			return nil, err
		}
		if spec.CertPEM, err = readEnvFile("PGGO_SSLCERT"); err != nil {
			return nil, err
		}
		if spec.KeyPEM, err = readEnvFile("PGGO_SSLKEY"); err != nil {
			return nil, err
		}
	}
	return spec, nil
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func readEnvFile(key string) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(key))
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s 读取失败: %w", key, err)
	}
	if len(data) > maxPEMSize {
		return nil, errors.New(key + " 文件过大(上限 256KB)")
	}
	return data, nil
}
