package users

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"project/database"
	"project/models"
	"project/utils"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	minTransfer float64 = 10000
	maxTransfer float64 = 10000000
)

// normalizePhoneNumber converts 0812241231, +62812241231, 62812241231 to 812241231
func normalizePhoneNumber(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "+", "")
	s = strings.ReplaceAll(s, " ", "")
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s = b.String()
	s = strings.TrimPrefix(s, "0")
	s = strings.TrimPrefix(s, "62")
	return s
}

type TransferInquiryRequest struct {
	Number string `json:"number"`
}

// TransferInquiryHandler POST /transfer/inquiry
// Body: { "number": "0812241231" }
// Returns: { name, number } or 404 if not found
func TransferInquiryHandler(w http.ResponseWriter, r *http.Request) {
	var req TransferInquiryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Format data tidak valid"})
		return
	}

	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	// VIP level 3 required for transfer
	var user models.User
	if err := database.DB.Select("level").First(&user, uid).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "User tidak ditemukan"})
			return
		}
		log.Printf("[transfer/inquiry] DB error fetching user %d: %v", uid, err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}
	userLevel := uint(0)
	if user.Level != nil {
		userLevel = *user.Level
	}
	if userLevel < 3 {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Anda belum memenuhi syarat untuk menggunakan fitur Transfer"})
		return
	}

	normalized := normalizePhoneNumber(req.Number)
	if normalized == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Nomor tidak valid"})
		return
	}

	// Cannot transfer to self
	var receiver models.User
	if err := database.DB.Select("id, name, number, profile").Where("number = ?", normalized).First(&receiver).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Pengguna tidak ditemukan"})
			return
		}
		log.Printf("[transfer/inquiry] DB error fetching receiver by number %s: %v", normalized, err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	if receiver.ID == uid {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Tidak dapat transfer ke nomor sendiri"})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data: map[string]interface{}{
			"name":    receiver.Name,
			"number":  receiver.Number,
			"profile": receiver.Profile,
		},
	})
}

type TransferRequest struct {
	Number string  `json:"number"`
	Amount float64 `json:"amount"`
}

// TransferHandler POST /transfer
// Body: { "number": "0812241231", "amount": 50000 }
func TransferHandler(w http.ResponseWriter, r *http.Request) {
	var req TransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Format data tidak valid"})
		return
	}

	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	normalized := normalizePhoneNumber(req.Number)
	if normalized == "" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Nomor tidak valid"})
		return
	}

	if req.Amount < minTransfer {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: fmt.Sprintf("Minimal transfer Rp %.0f", minTransfer),
		})
		return
	}
	if req.Amount > maxTransfer {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{
			Success: false,
			Message: fmt.Sprintf("Maksimal transfer Rp %.0f", maxTransfer),
		})
		return
	}

	var sender models.User
	if err := database.DB.First(&sender, uid).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "User tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	// VIP level 3 required for transfer
	senderLevel := uint(0)
	if sender.Level != nil {
		senderLevel = *sender.Level
	}
	if senderLevel < 3 {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Anda belum memenuhi syarat untuk menggunakan fitur Transfer"})
		return
	}

	var receiver models.User
	if err := database.DB.First(&receiver, "number = ?", normalized).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "Pengguna tidak ditemukan"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	if receiver.ID == uid {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Tidak dapat transfer ke nomor sendiri"})
		return
	}

	errInsufficient := errors.New("insufficient_balance")
	charge := 0.0

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		// Lock sender and receiver
		var lockedSender, lockedReceiver models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedSender, uid).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedReceiver, receiver.ID).Error; err != nil {
			return err
		}

		if lockedSender.Balance < req.Amount {
			return errInsufficient
		}

		senderNewBalance := round2(lockedSender.Balance - req.Amount)
		receiverNewBalance := round2(lockedReceiver.Balance + req.Amount)

		if err := tx.Model(&lockedSender).Update("balance", senderNewBalance).Error; err != nil {
			return err
		}
		if err := tx.Model(&lockedReceiver).Update("balance", receiverNewBalance).Error; err != nil {
			return err
		}

		orderID := utils.GenerateOrderID(uid)

		// Transfer record for sender: credit, type transfer
		msgSender := fmt.Sprintf("Transfer Uang ke %s", lockedReceiver.Name)
		trxSender := models.Transaction{
			UserID:          uid,
			Amount:          req.Amount,
			Charge:          charge,
			OrderID:         orderID,
			TransactionFlow: "credit",
			TransactionType: "transfer",
			Message:         &msgSender,
			Status:          "Success",
		}
		if err := tx.Create(&trxSender).Error; err != nil {
			return err
		}

		// Receive record for receiver: debit, type receive
		orderIDReceiver := utils.GenerateOrderID(receiver.ID)
		msgReceiver := fmt.Sprintf("Terima Uang dari %s", lockedSender.Name)
		trxReceiver := models.Transaction{
			UserID:          receiver.ID,
			Amount:          req.Amount,
			Charge:          charge,
			OrderID:         orderIDReceiver,
			TransactionFlow: "debit",
			TransactionType: "receive",
			Message:         &msgReceiver,
			Status:          "Success",
		}
		if err := tx.Create(&trxReceiver).Error; err != nil {
			return err
		}

		// Save transfer contact for history
		var contact models.TransferContact
		if err := tx.Where("sender_id = ? AND receiver_id = ?", uid, receiver.ID).First(&contact).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				tx.Create(&models.TransferContact{SenderID: uid, ReceiverID: receiver.ID})
			}
		} else {
			tx.Model(&contact).Updates(map[string]interface{}{"updated_at": time.Now()})
		}

		return nil
	}); err != nil {
		if errors.Is(err, errInsufficient) {
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Saldo tidak mencukupi"})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Transfer berhasil",
		Data: map[string]interface{}{
			"amount":    req.Amount,
			"charge":    0,
			"recipient": receiver.Name,
			"number":    receiver.Number,
		},
	})
}

// TransferContactHandler GET /transfer/contact
// Returns list of users that the current user has transferred to (latest first), with current user data
func TransferContactHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	// VIP level 3 required
	var user models.User
	if err := database.DB.Select("level").First(&user, uid).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}
	userLevel := uint(0)
	if user.Level != nil {
		userLevel = *user.Level
	}
	if userLevel < 3 {
		utils.WriteJSON(w, http.StatusForbidden, utils.APIResponse{Success: false, Message: "Anda belum memenuhi syarat untuk menggunakan fitur Transfer"})
		return
	}

	var contacts []models.TransferContact
	if err := database.DB.Where("sender_id = ?", uid).Order("updated_at DESC").Find(&contacts).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem"})
		return
	}

	if len(contacts) == 0 {
		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: []interface{}{}})
		return
	}

	receiverIDs := make([]uint, 0, len(contacts))
	seen := make(map[uint]struct{})
	for _, c := range contacts {
		if _, ok := seen[c.ReceiverID]; !ok {
			seen[c.ReceiverID] = struct{}{}
			receiverIDs = append(receiverIDs, c.ReceiverID)
		}
	}

	var users []models.User
	database.DB.Select("id, name, number, profile").Where("id IN ?", receiverIDs).Find(&users)
	userMap := make(map[uint]models.User)
	for _, u := range users {
		userMap[u.ID] = u
	}

	// Maintain order by updated_at (first occurrence)
	resp := make([]map[string]interface{}, 0, len(receiverIDs))
	for _, rid := range receiverIDs {
		if u, ok := userMap[rid]; ok {
			resp = append(resp, map[string]interface{}{
				"id":      u.ID,
				"name":    u.Name,
				"number":  u.Number,
				"profile": u.Profile,
			})
		}
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: resp})
}
