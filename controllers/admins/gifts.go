package admins

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"project/database"
	"project/models"
	"project/utils"

	"github.com/gorilla/mux"
	"gorm.io/gorm"
)

// GiftResponse for admin list/detail
type GiftResponse struct {
	ID               uint    `json:"id"`
	UserID           uint    `json:"user_id"`
	UserName         string  `json:"user_name"`
	UserPhone        string  `json:"user_phone"`
	Code             string  `json:"code"`
	Amount           float64 `json:"amount"`
	WinnerCount      int     `json:"winner_count"`
	ClaimedCount     int     `json:"claimed_count"`
	Remaining        int     `json:"remaining"`
	DistributionType string  `json:"distribution_type"`
	RecipientType    string  `json:"recipient_type"`
	Status           string  `json:"status"`
	TotalDeducted    float64 `json:"total_deducted"`
	CreatedAt        string  `json:"created_at"`
}

// GetGifts GET /admin/gifts - list all gifts with filters
func GetGifts(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	code := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code")))
	userID := r.URL.Query().Get("user_id")
	startDate := r.URL.Query().Get("start_date")
	endDate := r.URL.Query().Get("end_date")

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	query := database.DB.Model(&models.Gift{}).Joins("LEFT JOIN users ON gifts.user_id = users.id")

	if status != "" {
		query = query.Where("gifts.status = ?", status)
	}
	if code != "" {
		query = query.Where("gifts.code LIKE ?", "%"+code+"%")
	}
	if userID != "" {
		query = query.Where("gifts.user_id = ?", userID)
	}

	jakartaLoc, _ := time.LoadLocation("Asia/Jakarta")
	if startDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", startDate, jakartaLoc); err == nil {
			query = query.Where("gifts.created_at >= ?", t)
		}
	}
	if endDate != "" {
		if t, err := time.ParseInLocation("2006-01-02", endDate, jakartaLoc); err == nil {
			t = t.AddDate(0, 0, 1)
			query = query.Where("gifts.created_at < ?", t)
		}
	}

	var total int64
	query.Count(&total)

	var gifts []models.Gift
	if err := query.Select("gifts.*").
		Order("gifts.created_at DESC").
		Offset(offset).Limit(limit).
		Find(&gifts).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data hadiah",
		})
		return
	}

	// Build user map
	userIDs := make([]uint, 0, len(gifts))
	for _, g := range gifts {
		userIDs = append(userIDs, g.UserID)
	}
	usersMap := make(map[uint]struct{ Name, Number string })
	if len(userIDs) > 0 {
		var users []models.User
		database.DB.Select("id, name, number").Where("id IN ?", userIDs).Find(&users)
		for _, u := range users {
			usersMap[u.ID] = struct{ Name, Number string }{u.Name, u.Number}
		}
	}

	items := make([]GiftResponse, 0, len(gifts))
	for _, g := range gifts {
		var claimed int64
		database.DB.Model(&models.GiftClaim{}).Where("gift_id = ?", g.ID).Count(&claimed)
		u := usersMap[g.UserID]
		items = append(items, GiftResponse{
			ID:               g.ID,
			UserID:           g.UserID,
			UserName:         u.Name,
			UserPhone:        u.Number,
			Code:             g.Code,
			Amount:           g.Amount,
			WinnerCount:      g.WinnerCount,
			ClaimedCount:     int(claimed),
			Remaining:        g.WinnerCount - int(claimed),
			DistributionType: g.DistributionType,
			RecipientType:    g.RecipientType,
			Status:           g.Status,
			TotalDeducted:    g.TotalDeducted,
			CreatedAt:        g.CreatedAt.Format(time.RFC3339),
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data: map[string]interface{}{
			"items": items,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// GetGiftDetail GET /admin/gifts/{id} - get gift detail by ID
func GetGiftDetail(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]
	if idStr == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "ID hadiah wajib"})
		return
	}
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "ID hadiah tidak valid"})
		return
	}

	var gift models.Gift
	if err := database.DB.Preload("User", func(db *gorm.DB) *gorm.DB {
		return db.Select("id, name, number, reff_code")
	}).Preload("Claims").Preload("Slots").First(&gift, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Hadiah tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil data hadiah"})
		return
	}

	claimedCount := len(gift.Claims)
	senderName := ""
	senderPhone := ""
	senderReffCode := ""
	if gift.User != nil {
		senderName = gift.User.Name
		senderPhone = gift.User.Number
		senderReffCode = gift.User.ReffCode
	}

	claimsData := make([]map[string]interface{}, 0, len(gift.Claims))
	for _, c := range gift.Claims {
		var claimer models.User
		database.DB.Select("id, name, number").First(&claimer, c.UserID)
		claimsData = append(claimsData, map[string]interface{}{
			"id":         c.ID,
			"user_id":    c.UserID,
			"user_name":  claimer.Name,
			"user_phone": claimer.Number,
			"amount":     c.Amount,
			"slot_index": c.SlotIndex,
			"created_at": c.CreatedAt.Format(time.RFC3339),
		})
	}

	slotsData := make([]map[string]interface{}, 0, len(gift.Slots))
	for _, s := range gift.Slots {
		slotsData = append(slotsData, map[string]interface{}{
			"slot_index": s.SlotIndex,
			"amount":     s.Amount,
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Data: map[string]interface{}{
			"id":                gift.ID,
			"user_id":           gift.UserID,
			"sender_name":       senderName,
			"sender_phone":      senderPhone,
			"sender_reff_code":  senderReffCode,
			"code":              gift.Code,
			"amount":            gift.Amount,
			"winner_count":      gift.WinnerCount,
			"claimed_count":     claimedCount,
			"remaining":         gift.WinnerCount - claimedCount,
			"distribution_type": gift.DistributionType,
			"recipient_type":    gift.RecipientType,
			"status":            gift.Status,
			"total_deducted":    gift.TotalDeducted,
			"created_at":        gift.CreatedAt.Format(time.RFC3339),
			"claims":            claimsData,
			"slots":             slotsData,
		},
	})
}

// GetGiftWinners GET /admin/gifts/{id}/winners - get winners of a gift
func GetGiftWinners(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]
	if idStr == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "ID hadiah wajib"})
		return
	}
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "ID hadiah tidak valid"})
		return
	}

	var gift models.Gift
	if err := database.DB.First(&gift, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Hadiah tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil data hadiah"})
		return
	}

	var claims []models.GiftClaim
	if err := database.DB.Preload("User", func(db *gorm.DB) *gorm.DB {
		return db.Select("id, name, number, profile")
	}).Where("gift_id = ?", gift.ID).Order("slot_index").Find(&claims).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil data pemenang"})
		return
	}

	winners := make([]map[string]interface{}, 0, len(claims))
	for _, c := range claims {
		u := c.User
		name := ""
		number := ""
		profile := interface{}(nil)
		if u != nil {
			name = u.Name
			number = u.Number
			profile = u.Profile
		}
		winners = append(winners, map[string]interface{}{
			"user_id":    c.UserID,
			"name":       name,
			"number":     number,
			"profile":    profile,
			"amount":     c.Amount,
			"slot":       c.SlotIndex + 1,
			"claimed_at": c.CreatedAt.Format(time.RFC3339),
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Data: map[string]interface{}{
			"gift": map[string]interface{}{
				"id":            gift.ID,
				"code":          gift.Code,
				"status":        gift.Status,
				"claimed_count": len(claims),
				"winner_count":  gift.WinnerCount,
			},
			"winners": winners,
		},
	})
}

// CancelGift PUT /admin/gifts/{id}/cancel - cancel active gift
func CancelGift(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]
	if idStr == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "ID hadiah wajib"})
		return
	}
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "ID hadiah tidak valid"})
		return
	}

	var gift models.Gift
	if err := database.DB.First(&gift, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Hadiah tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil data hadiah"})
		return
	}

	if gift.Status != "active" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Hadiah hanya dapat dibatalkan jika status active",
		})
		return
	}

	var claimedCount int64
	database.DB.Model(&models.GiftClaim{}).Where("gift_id = ?", gift.ID).Count(&claimedCount)
	if claimedCount > 0 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: "Hadiah yang sudah ada klaim tidak dapat dibatalkan",
		})
		return
	}

	// Refund balance to sender
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&gift).Update("status", "cancelled").Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", gift.UserID).
			Update("balance", gorm.Expr("balance + ?", gift.TotalDeducted)).Error; err != nil {
			return err
		}
		msg := "Refund hadiah dibatalkan - " + gift.Code
		trx := models.Transaction{
			UserID:          gift.UserID,
			Amount:          gift.TotalDeducted,
			Charge:          0,
			OrderID:         utils.GenerateOrderID(gift.UserID),
			TransactionFlow: "debit",
			TransactionType: "refund",
			Message:         &msg,
			Status:          "Success",
		}
		return tx.Create(&trx).Error
	})

	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal membatalkan hadiah",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Hadiah berhasil dibatalkan dan saldo telah dikembalikan",
		Data: map[string]interface{}{
			"id":     gift.ID,
			"code":   gift.Code,
			"status": "cancelled",
		},
	})
}
