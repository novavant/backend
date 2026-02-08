package users

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"project/database"
	"project/models"
	"project/utils"
	"strconv"

	"github.com/gorilla/mux"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	minGiftAmount   float64 = 1000
	maxGiftAmount   float64 = 10000000
	minGiftWinners  int     = 1
	maxGiftWinners  int     = 100
	minRandomAmount float64 = 100 // minimum per winner for random mode
)

// CreateGiftRequest for POST /gift
// random: amount=total, winner_count=jumlah pemenang
// equal: amount=per pemenang, winner_count=jumlah pemenang
type CreateGiftRequest struct {
	Amount           float64 `json:"amount"`
	WinnerCount      int     `json:"winner_count"`
	DistributionType string  `json:"distribution_type"` // "random" | "equal"
	RecipientType    string  `json:"recipient_type"`    // "all" | "referral_only"
}

// CreateGiftHandler POST /gift - create gift
func CreateGiftHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	var req CreateGiftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Format data tidak valid"})
		return
	}

	// Validations
	req.DistributionType = strings.ToLower(strings.TrimSpace(req.DistributionType))
	req.RecipientType = strings.ToLower(strings.TrimSpace(req.RecipientType))

	if req.DistributionType != "random" && req.DistributionType != "equal" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Tipe distribusi harus random atau equal"})
		return
	}
	if req.RecipientType != "all" && req.RecipientType != "referral_only" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Penerima harus all atau referral_only"})
		return
	}
	if req.WinnerCount < minGiftWinners || req.WinnerCount > maxGiftWinners {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: fmt.Sprintf("Jumlah pemenang harus antara %d dan %d", minGiftWinners, maxGiftWinners)})
		return
	}
	if req.Amount < minGiftAmount || req.Amount > maxGiftAmount {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: fmt.Sprintf("Jumlah harus antara Rp %.0f dan Rp %.0f", minGiftAmount, maxGiftAmount)})
		return
	}

	// Calculate total to deduct
	var totalDeducted float64
	if req.DistributionType == "random" {
		totalDeducted = req.Amount
		// Min per winner check
		if req.Amount < float64(req.WinnerCount)*minRandomAmount {
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: fmt.Sprintf("Total minimal Rp %.0f untuk %d pemenang (Rp %.0f per pemenang)", float64(req.WinnerCount)*minRandomAmount, req.WinnerCount, minRandomAmount)})
			return
		}
	} else {
		totalDeducted = req.Amount * float64(req.WinnerCount)
	}

	// Check sender balance
	var sender models.User
	if err := database.DB.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sender, uid).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Pengguna tidak ditemukan"})
			return
		}
		log.Printf("[gift/create] DB error: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	if sender.Balance < totalDeducted {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Saldo tidak mencukupi"})
		return
	}

	// Only level 1+ can create gift
	userLevel := uint(0)
	if sender.Level != nil {
		userLevel = *sender.Level
	}
	if userLevel < 1 {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Fitur gift hanya untuk VIP level 1 ke atas"})
		return
	}

	// Check promotor mode - gift may not be allowed for promotor
	if strings.ToLower(sender.UserMode) == "promotor" {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Fitur hadiah tidak tersedia untuk mode promotor"})
		return
	}

	// Generate unique gift code
	code, err := generateGiftCode()
	if err != nil {
		log.Printf("[gift/create] generate code error: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal membuat kode hadiah"})
		return
	}

	// Prepare gift and slots (for random)
	var slots []models.GiftAmountSlot
	if req.DistributionType == "random" {
		slots = generateRandomAmounts(req.Amount, req.WinnerCount)
	}

	gift := models.Gift{
		UserID:           uid,
		Code:             code,
		Amount:           req.Amount,
		WinnerCount:      req.WinnerCount,
		DistributionType: req.DistributionType,
		RecipientType:    req.RecipientType,
		Status:           "active",
		TotalDeducted:    totalDeducted,
	}

	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Deduct balance
		if err := tx.Model(&sender).Update("balance", gorm.Expr("balance - ?", totalDeducted)).Error; err != nil {
			return err
		}
		// Create gift
		if err := tx.Create(&gift).Error; err != nil {
			return err
		}
		// Create slots for random
		for i := range slots {
			slots[i].GiftID = gift.ID
			if err := tx.Create(&slots[i]).Error; err != nil {
				return err
			}
		}
		// Create transaction record
		msg := fmt.Sprintf("Hadiah %s - %s", code, req.DistributionType)
		trx := models.Transaction{
			UserID:          uid,
			Amount:          totalDeducted,
			Charge:          0,
			OrderID:         utils.GenerateOrderID(uid),
			TransactionFlow: "credit",
			TransactionType: "gift",
			Message:         &msg,
			Status:          "Success",
		}
		return tx.Create(&trx).Error
	})

	if err != nil {
		log.Printf("[gift/create] transaction error: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal membuat hadiah"})
		return
	}

	utils.WriteJSON(w, http.StatusCreated, utils.APIResponse{
		Success: true,
		Message: "Hadiah berhasil dibuat",
		Data: map[string]interface{}{
			"id":                gift.ID,
			"code":              gift.Code,
			"amount":            gift.Amount,
			"winner_count":      gift.WinnerCount,
			"distribution_type": gift.DistributionType,
			"recipient_type":    gift.RecipientType,
			"total_deducted":    gift.TotalDeducted,
			"status":            gift.Status,
			"created_at":        gift.CreatedAt.Format(time.RFC3339),
		},
	})
}

// generateRandomAmounts splits total into winnerCount amounts (fair random distribution)
// Each winner gets at least minRandomAmount, total exactly equals input
func generateRandomAmounts(total float64, winnerCount int) []models.GiftAmountSlot {
	slots := make([]models.GiftAmountSlot, winnerCount)
	weights := make([]float64, winnerCount)
	var sum float64

	for i := 0; i < winnerCount; i++ {
		b := make([]byte, 8)
		rand.Read(b)
		w := float64(0)
		for _, v := range b {
			w = w*256 + float64(v)
		}
		weights[i] = w + 1
		sum += weights[i]
	}

	// Assign proportional amounts (floored)
	remaining := total
	for i := 0; i < winnerCount-1; i++ {
		prop := total * weights[i] / sum
		slotAmount := math.Floor(prop)
		if slotAmount < minRandomAmount {
			slotAmount = minRandomAmount
		}
		remaining -= slotAmount
		slots[i] = models.GiftAmountSlot{SlotIndex: i, Amount: slotAmount}
	}
	// Last slot gets remainder (ensure >= minRandomAmount)
	if remaining < minRandomAmount {
		// Take from largest previous slot
		maxIdx := 0
		for i := 1; i < winnerCount-1; i++ {
			if slots[i].Amount > slots[maxIdx].Amount {
				maxIdx = i
			}
		}
		need := minRandomAmount - remaining
		slots[maxIdx].Amount -= need
		remaining += need
	}
	slots[winnerCount-1] = models.GiftAmountSlot{SlotIndex: winnerCount - 1, Amount: remaining}

	// Adjust for floating point: ensure sum exactly equals total
	actualSum := 0.0
	for _, s := range slots {
		actualSum += s.Amount
	}
	diff := total - actualSum
	if diff != 0 {
		slots[winnerCount-1].Amount += diff
		if slots[winnerCount-1].Amount < minRandomAmount {
			slots[winnerCount-1].Amount = minRandomAmount
		}
	}

	return slots
}

func generateGiftCode() (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	const length = 8
	for attempt := 0; attempt < 20; attempt++ {
		b := make([]byte, length)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		var code strings.Builder
		for _, v := range b {
			code.WriteByte(chars[int(v)%len(chars)])
		}
		c := code.String()
		var count int64
		if database.DB.Model(&models.Gift{}).Where("code = ?", c).Count(&count).Error == nil && count == 0 {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not generate unique gift code")
}

// RedeemGiftRequest for POST /gift/redeem
type RedeemGiftRequest struct {
	Code string `json:"code"`
}

// RedeemGiftHandler POST /gift/redeem - claim gift
func RedeemGiftHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	var req RedeemGiftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Format data tidak valid"})
		return
	}

	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Kode hadiah tidak boleh kosong"})
		return
	}

	var gift models.Gift
	if err := database.DB.Preload("Claims").First(&gift, "code = ?", code).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Hadiah tidak ditemukan"})
			return
		}
		log.Printf("[gift/redeem] DB error: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	if gift.Status != "active" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Hadiah sudah tidak aktif"})
		return
	}

	// Cannot claim own gift
	if gift.UserID == uid {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Tidak dapat mengklaim hadiah sendiri"})
		return
	}

	// Check if already claimed
	var existingClaim models.GiftClaim
	if err := database.DB.Where("gift_id = ? AND user_id = ?", gift.ID, uid).First(&existingClaim).Error; err == nil {
		utils.WriteJSON(w, http.StatusConflict, utils.APIResponse{Success: false, Message: "Anda sudah mengklaim hadiah ini"})
		return
	}

	// Check eligibility: recipient_type
	var claimant models.User
	if err := database.DB.Select("id, reff_by, user_mode").First(&claimant, uid).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Pengguna tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	if gift.RecipientType == "referral_only" {
		if claimant.ReffBy == nil || *claimant.ReffBy != gift.UserID {
			utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Hadiah ini hanya untuk referral pengirim"})
			return
		}
	}

	// Promotor cannot claim
	if strings.ToLower(claimant.UserMode) == "promotor" {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Mode promotor tidak dapat mengklaim hadiah"})
		return
	}

	// Get amount and slot (for random: next slot; for equal: fixed amount)
	var amount float64
	var slotIndex int
	claimedCount := len(gift.Claims)

	if claimedCount >= gift.WinnerCount {
		utils.WriteJSON(w, http.StatusGone, utils.APIResponse{Success: false, Message: "Hadiah sudah habis"})
		return
	}

	if gift.DistributionType == "random" {
		var slot models.GiftAmountSlot
		if err := database.DB.Where("gift_id = ?", gift.ID).Order("slot_index").Offset(claimedCount).First(&slot).Error; err != nil {
			log.Printf("[gift/redeem] slot error: %v", err)
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
			return
		}
		amount = slot.Amount
		slotIndex = slot.SlotIndex
	} else {
		amount = gift.Amount
		slotIndex = claimedCount
	}

	// Create claim and credit balance
	claim := models.GiftClaim{
		GiftID:    gift.ID,
		UserID:    uid,
		Amount:    amount,
		SlotIndex: slotIndex,
	}

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&claim).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", uid).Update("balance", gorm.Expr("balance + ?", amount)).Error; err != nil {
			return err
		}
		msg := fmt.Sprintf("Hadiah dari %s", code)
		trx := models.Transaction{
			UserID:          uid,
			Amount:          amount,
			Charge:          0,
			OrderID:         utils.GenerateOrderID(uid),
			TransactionFlow: "debit",
			TransactionType: "bonus",
			Message:         &msg,
			Status:          "Success",
		}
		if err := tx.Create(&trx).Error; err != nil {
			return err
		}
		// Mark gift completed if all claimed
		if claimedCount+1 >= gift.WinnerCount {
			return tx.Model(&gift).Update("status", "completed").Error
		}
		return nil
	})

	if err != nil {
		log.Printf("[gift/redeem] transaction error: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengklaim hadiah"})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Selamat! Anda berhasil mengklaim hadiah",
		Data: map[string]interface{}{
			"amount":  amount,
			"code":    code,
			"gift_id": gift.ID,
		},
	})
}

// GiftInquiryHandler GET /gift/inquiry?code=XXX - check gift info
func GiftInquiryHandler(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code")))
	if code == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Parameter code wajib"})
		return
	}

	var gift models.Gift
	if err := database.DB.Preload("User", func(db *gorm.DB) *gorm.DB {
		return db.Select("id, name, profile")
	}).Preload("Claims").First(&gift, "code = ?", code).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Hadiah tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	claimedCount := len(gift.Claims)
	canClaim := gift.Status == "active" && claimedCount < gift.WinnerCount

	// Check if current user can claim (if authenticated)
	uid, hasAuth := utils.GetUserID(r)
	reason := ""
	if hasAuth && uid != 0 {
		if gift.UserID == uid {
			canClaim = false
			reason = "Anda adalah pengirim hadiah"
		} else if canClaim {
			var existing models.GiftClaim
			if database.DB.Where("gift_id = ? AND user_id = ?", gift.ID, uid).First(&existing).Error == nil {
				canClaim = false
				reason = "Anda sudah mengklaim hadiah ini"
			}
		}
	} else if canClaim {
		reason = "Login untuk mengklaim"
	}

	senderName := ""
	if gift.User != nil {
		senderName = gift.User.Name
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Data: map[string]interface{}{
			"id":                gift.ID,
			"code":              gift.Code,
			"amount":            gift.Amount,
			"winner_count":      gift.WinnerCount,
			"claimed_count":     claimedCount,
			"remaining":         gift.WinnerCount - claimedCount,
			"distribution_type": gift.DistributionType,
			"recipient_type":    gift.RecipientType,
			"status":            gift.Status,
			"total_deducted":    gift.TotalDeducted,
			"sender_name":       senderName,
			"can_claim":         canClaim,
			"reason":            reason,
			"created_at":        gift.CreatedAt.Format(time.RFC3339),
		},
	})
}

// GiftHistoryHandler GET /gift/history - gifts created by user
func GiftHistoryHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	page, _ := parseInt(r.URL.Query().Get("page"), 1)
	limit, _ := parseInt(r.URL.Query().Get("limit"), 20)
	if limit > 50 {
		limit = 50
	}
	offset := (page - 1) * limit

	var gifts []models.Gift
	var total int64
	database.DB.Model(&models.Gift{}).Where("user_id = ?", uid).Count(&total)
	if err := database.DB.Where("user_id = ?", uid).Order("created_at DESC").Offset(offset).Limit(limit).Find(&gifts).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	// Get claimed count per gift
	items := make([]map[string]interface{}, 0, len(gifts))
	for _, g := range gifts {
		var claimed int64
		database.DB.Model(&models.GiftClaim{}).Where("gift_id = ?", g.ID).Count(&claimed)
		items = append(items, map[string]interface{}{
			"id":                g.ID,
			"code":              g.Code,
			"amount":            g.Amount,
			"winner_count":      g.WinnerCount,
			"claimed_count":     claimed,
			"remaining":         g.WinnerCount - int(claimed),
			"distribution_type": g.DistributionType,
			"recipient_type":    g.RecipientType,
			"status":            g.Status,
			"total_deducted":    g.TotalDeducted,
			"created_at":        g.CreatedAt.Format(time.RFC3339),
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Data: map[string]interface{}{
			"items": items,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// GiftWinsHandler GET /gift/wins - gifts user has claimed
func GiftWinsHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	page, _ := parseInt(r.URL.Query().Get("page"), 1)
	limit, _ := parseInt(r.URL.Query().Get("limit"), 20)
	if limit > 50 {
		limit = 50
	}
	offset := (page - 1) * limit

	var claims []models.GiftClaim
	var total int64
	database.DB.Model(&models.GiftClaim{}).Where("user_id = ?", uid).Count(&total)
	if err := database.DB.Preload("Gift").Preload("Gift.User", func(db *gorm.DB) *gorm.DB {
		return db.Select("id, name")
	}).Where("user_id = ?", uid).Order("created_at DESC").Offset(offset).Limit(limit).Find(&claims).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	items := make([]map[string]interface{}, 0, len(claims))
	for _, c := range claims {
		senderName := ""
		code := ""
		if c.Gift != nil {
			code = c.Gift.Code
			if c.Gift.User != nil {
				senderName = c.Gift.User.Name
			}
		}
		items = append(items, map[string]interface{}{
			"id":         c.ID,
			"gift_id":    c.GiftID,
			"code":       code,
			"amount":     c.Amount,
			"sender":     senderName,
			"created_at": c.CreatedAt.Format(time.RFC3339),
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Data: map[string]interface{}{
			"items": items,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// GiftWinnersHandler GET /gift/{id}/winners - winners of a gift (for sender)
func GiftWinnersHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Hadiah tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	if gift.UserID != uid {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Anda bukan pengirim hadiah ini"})
		return
	}

	var claims []models.GiftClaim
	if err := database.DB.Preload("User", func(db *gorm.DB) *gorm.DB {
		return db.Select("id, name, number, profile")
	}).Where("gift_id = ?", gift.ID).Order("slot_index").Find(&claims).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
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
			number = maskNumber(u.Number)
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

func maskNumber(s string) string {
	if len(s) < 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

func parseInt(s string, defaultVal int) (int, bool) {
	if s == "" {
		return defaultVal, false
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return defaultVal, false
	}
	return n, true
}
