package api

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultIdleTimeout       = 2 * time.Minute
	defaultMaxHeaderBytes    = 64 << 10
)

func configureTrustedProxies(engine *gin.Engine, configured []string) {
	if engine == nil {
		return
	}
	proxies := normalizeTrustedProxies(configured)
	if errSet := engine.SetTrustedProxies(proxies); errSet != nil {
		log.WithError(errSet).Warn("invalid trusted-proxies configuration; forwarded client IP headers are disabled")
		if errDisable := engine.SetTrustedProxies(nil); errDisable != nil {
			log.WithError(errDisable).Error("failed to disable trusted proxies")
		}
	}
}

func normalizeTrustedProxies(configured []string) []string {
	if len(configured) == 0 {
		return nil
	}
	proxies := make([]string, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for _, proxy := range configured {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" {
			continue
		}
		if _, exists := seen[proxy]; exists {
			continue
		}
		seen[proxy] = struct{}{}
		proxies = append(proxies, proxy)
	}
	if len(proxies) == 0 {
		return nil
	}
	return proxies
}
