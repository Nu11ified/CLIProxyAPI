package api

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed routing_dashboard.html
var routingDashboard embed.FS

func (s *Server) serveRoutingDashboard(c *gin.Context) {
	if s.cfg == nil || s.cfg.Home.Enabled || s.cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	page, err := routingDashboard.ReadFile("routing_dashboard.html")
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", page)
}
