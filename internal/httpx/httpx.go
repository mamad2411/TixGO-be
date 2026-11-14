// Package httpx = helper HTTP tipis: envelope respons, bind+validasi, paginasi, konteks principal.
package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"tixgo/backend/internal/apperr"
	"tixgo/backend/internal/auth"
)

const (
	principalKey = "tixgo.principal"
	requestIDKey = "tixgo.request_id"
)

// Meta = metadata paginasi (docs/08 §1).
type Meta struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

func OK(c *gin.Context, data any)      { c.JSON(http.StatusOK, gin.H{"data": data}) }
func Created(c *gin.Context, data any) { c.JSON(http.StatusCreated, gin.H{"data": data}) }
func Accepted(c *gin.Context, data any) {
	c.JSON(http.StatusAccepted, gin.H{"data": data})
}
func NoContent(c *gin.Context) { c.Status(http.StatusNoContent) }

func List(c *gin.Context, items any, m Meta) {
	c.JSON(http.StatusOK, gin.H{"data": items, "meta": m})
}

// Fail memetakan error ke respons standar. Error non-apperr menjadi 500 generik (dicatat).
func Fail(c *gin.Context, err error) {
	if err == nil {
		return
	}
	ae, ok := apperr.As(err)
	if !ok {
		ae = apperr.Internal(err)
	}
	if ae.Status >= 500 {
		slog.Error("request gagal", "path", c.FullPath(), "request_id", RequestID(c), "error", err)
	}
	body := gin.H{"code": ae.Code, "message": ae.Message}
	if rid := RequestID(c); rid != "" {
		body["request_id"] = rid // rujukan untuk pengguna/support (M28 AC6)
	}
	if len(ae.Details) > 0 {
		body["details"] = ae.Details
	}
	if ae.Status == http.StatusTooManyRequests {
		if ra, ok := ae.Details["retry_after"].(int); ok {
			c.Header("Retry-After", strconv.Itoa(ra))
		}
	}
	c.AbortWithStatusJSON(ae.Status, gin.H{"error": body})
}

// Bind membaca JSON body, memvalidasi tag `binding`, dan mengembalikan apperr VALIDATION_FAILED.
func Bind[T any](c *gin.Context) (T, bool) {
	var req T
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, validationError(err))
		return req, false
	}
	return req, true
}

// BindQuery membaca query string.
func BindQuery[T any](c *gin.Context) (T, bool) {
	var req T
	if err := c.ShouldBindQuery(&req); err != nil {
		Fail(c, validationError(err))
		return req, false
	}
	return req, true
}

func validationError(err error) *apperr.Error {
	var ve validator.ValidationErrors
	if errors.As(err, &ve) {
		details := make(map[string]any, len(ve))
		for _, fe := range ve {
			field := snake(fe.Field())
			rule := fe.Tag()
			if fe.Param() != "" {
				rule += "=" + fe.Param()
			}
			details[field] = rule
		}
		e := apperr.Validation("data tidak valid")
		e.Details = details
		return e
	}
	return apperr.Validation("body tidak valid: " + shorten(err.Error(), 160))
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Page membaca ?page&per_page (default 1/20, maks 100).
func Page(c *gin.Context) (page, perPage, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ = strconv.Atoi(c.DefaultQuery("per_page", "20"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage, (page - 1) * perPage
}

// SetPrincipal / Principal: identitas terautentikasi pada konteks request.
func SetPrincipal(c *gin.Context, p *auth.Principal) { c.Set(principalKey, p) }

func Principal(c *gin.Context) *auth.Principal {
	v, ok := c.Get(principalKey)
	if !ok {
		return nil
	}
	p, _ := v.(*auth.Principal)
	return p
}

// MustPrincipal mengembalikan principal; bila tidak ada, menulis 401 dan mengembalikan nil.
func MustPrincipal(c *gin.Context) *auth.Principal {
	p := Principal(c)
	if p == nil {
		Fail(c, apperr.Unauthorized("login diperlukan"))
	}
	return p
}

func SetRequestID(c *gin.Context, id string) { c.Set(requestIDKey, id); c.Header("X-Request-Id", id) }
func RequestID(c *gin.Context) string        { return c.GetString(requestIDKey) }

// Param membaca path param dan memvalidasi bentuk ULID (menolak input aneh sebelum menyentuh DB).
func ParamID(c *gin.Context, name string) (string, bool) {
	v := c.Param(name)
	if len(v) != 26 {
		Fail(c, apperr.NotFound("sumber daya"))
		return "", false
	}
	return v, true
}

// ClientIP mengembalikan IP klien (memakai trusted proxies yang dikonfigurasi pada Gin).
func ClientIP(c *gin.Context) string { return c.ClientIP() }

// Sprintf pembantu pesan.
func Sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
