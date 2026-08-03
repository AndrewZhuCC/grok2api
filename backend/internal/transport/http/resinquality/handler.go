package resinquality

import (
	"net/http"
	"strconv"
	"strings"

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
	router.PUT("/resin-quality-guard/probe-key", h.setProbeKey)
	router.POST("/resin-quality-guard/reshuffle", h.reshuffle)
	router.POST("/resin-quality-guard/probe", h.probe)
}

func (h *Handler) status(c *gin.Context) {
	if h.guard == nil {
		response.Success(c, http.StatusOK, gin.H{"available": false, "enabled": false})
		return
	}
	response.Success(c, http.StatusOK, h.guard.GetStatus(c.Request.Context()))
}

type setProbeKeyRequest struct {
	// KeyID empty or "0" / "auto" → auto-select first usable client key.
	KeyID string `json:"keyId"`
}

func (h *Handler) setProbeKey(c *gin.Context) {
	if h.guard == nil || !h.guard.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "resin_quality_guard_disabled", "Resin 质量守护未启用")
		return
	}
	var req setProbeKeyRequest
	_ = c.ShouldBindJSON(&req)
	raw := strings.TrimSpace(req.KeyID)
	var id uint64
	if raw != "" && raw != "0" && !strings.EqualFold(raw, "auto") {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			response.Error(c, http.StatusBadRequest, "invalidKeyId", "keyId 无效")
			return
		}
		id = parsed
	}
	h.guard.SetSelectedProbeKey(id)
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

func (h *Handler) probe(c *gin.Context) {
	if h.guard == nil || !h.guard.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "resin_quality_guard_disabled", "Resin 质量守护未启用")
		return
	}
	result, err := h.guard.ManualProbe(c.Request.Context())
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "probe not configured") {
			response.Error(c, http.StatusBadRequest, "probe_not_configured", "没有可用的客户端密钥，请先在「密钥」页创建启用一把 Key")
			return
		}
		response.Error(c, http.StatusBadGateway, "resin_probe_failed", msg)
		return
	}
	response.Success(c, http.StatusOK, result)
}
