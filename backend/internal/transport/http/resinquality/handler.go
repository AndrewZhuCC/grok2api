package resinquality

import (
	"net/http"

	resinqualityapp "github.com/chenyme/grok2api/backend/internal/application/resinquality"
	"github.com/chenyme/grok2api/backend/internal/shared/response"
	"github.com/gin-gonic/gin"
)

// Handler exposes admin APIs for the in-process Resin quality guard.
type Handler struct {
	guard *resinqualityapp.Guard
}

func NewHandler(guard *resinqualityapp.Guard) *Handler {
	return &Handler{guard: guard}
}

func (h *Handler) Register(router *gin.RouterGroup) {
	router.GET("/resin-quality-guard", h.status)
	router.POST("/resin-quality-guard/reshuffle", h.reshuffle)
	router.PUT("/resin-quality-guard/config", h.updateConfig)
}

func (h *Handler) status(c *gin.Context) {
	if h.guard == nil {
		response.Success(c, http.StatusOK, gin.H{"available": false, "enabled": false})
		return
	}
	response.Success(c, http.StatusOK, h.guard.GetStatus(c.Request.Context()))
}

func (h *Handler) reshuffle(c *gin.Context) {
	if h.guard == nil || !h.guard.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "resin_quality_guard_disabled", "Resin 质量守护未启用")
		return
	}
	result, err := h.guard.ManualReshuffle(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusBadGateway, "resin_reshuffle_failed", err.Error())
		return
	}
	response.Success(c, http.StatusOK, result)
}

type updateConfigRequest struct {
	StreamMaxAttempts *int `json:"streamMaxAttempts"`
}

func (h *Handler) updateConfig(c *gin.Context) {
	if h.guard == nil || !h.guard.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "resin_quality_guard_disabled", "Resin 质量守护未启用")
		return
	}
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.StreamMaxAttempts == nil {
		response.Error(c, http.StatusBadRequest, "invalid_request", "streamMaxAttempts is required (1-8)")
		return
	}
	n := h.guard.UpdateStreamMaxAttempts(*req.StreamMaxAttempts)
	response.Success(c, http.StatusOK, gin.H{
		"streamMaxAttempts": n,
		"config":            h.guard.GetStatus(c.Request.Context()).Config,
	})
}
