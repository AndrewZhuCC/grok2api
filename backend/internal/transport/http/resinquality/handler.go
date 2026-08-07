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
	StreamMaxAttempts  *int  `json:"streamMaxAttempts"`
	StreamWatchEnabled *bool `json:"streamWatchEnabled"`
}

func (h *Handler) updateConfig(c *gin.Context) {
	if h.guard == nil || !h.guard.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "resin_quality_guard_disabled", "Resin 质量守护未启用")
		return
	}
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.StreamMaxAttempts == nil && req.StreamWatchEnabled == nil {
		response.Error(c, http.StatusBadRequest, "invalid_request", "streamMaxAttempts and/or streamWatchEnabled is required")
		return
	}
	out := gin.H{"config": nil}
	if req.StreamMaxAttempts != nil {
		out["streamMaxAttempts"] = h.guard.UpdateStreamMaxAttempts(*req.StreamMaxAttempts)
	}
	if req.StreamWatchEnabled != nil {
		out["streamWatchEnabled"] = h.guard.UpdateStreamWatchEnabled(*req.StreamWatchEnabled)
	}
	status := h.guard.GetStatus(c.Request.Context())
	out["config"] = status.Config
	// Always echo effective values so UI can sync after partial updates.
	out["streamMaxAttempts"] = status.Config.StreamMaxAttempts
	out["streamWatchEnabled"] = status.Config.StreamWatchEnabled
	response.Success(c, http.StatusOK, out)
}
