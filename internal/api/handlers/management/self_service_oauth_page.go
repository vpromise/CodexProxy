package management

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed selfservice/index.html selfservice/app.js selfservice/style.css
var selfServiceAssets embed.FS

func (h *Handler) ServeSelfServiceOAuth(c *gin.Context) {
	selfServiceHeaders(c)
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	name, mime := "index.html", "text/html; charset=utf-8"
	switch c.Param("asset") {
	case "app.js":
		name, mime = "app.js", "text/javascript; charset=utf-8"
	case "style.css":
		name, mime = "style.css", "text/css; charset=utf-8"
	case "":
	default:
		c.Status(http.StatusNotFound)
		return
	}
	content, err := selfServiceAssets.ReadFile("selfservice/" + name)
	if err != nil {
		c.Status(500)
		return
	}
	c.Data(http.StatusOK, mime, content)
}
