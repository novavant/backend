package admins

import (
	"encoding/json"
	"log"
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

type WithdrawalResponse struct {
	ID            uint    `json:"id"`
	UserID        uint    `json:"user_id"`
	UserName      string  `json:"user_name"`
	Phone         string  `json:"phone"`
	BankAccountID uint    `json:"bank_account_id"`
	BankName      string  `json:"bank_name"`
	AccountName   string  `json:"account_name"`
	AccountNumber string  `json:"account_number"`
	Amount        float64 `json:"amount"`
	Charge        float64 `json:"charge"`
	FinalAmount   float64 `json:"final_amount"`
	OrderID       string  `json:"order_id"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
}

func GetWithdrawals(w http.ResponseWriter, r *http.Request) {
	// Get query parameters
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	status := r.URL.Query().Get("status")
	userID := r.URL.Query().Get("user_id")
	orderID := r.URL.Query().Get("search")

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	offset := (page - 1) * limit

	// Start query
	db := database.DB
	query := db.Model(&models.Withdrawal{}).
		Joins("JOIN users ON withdrawals.user_id = users.id").
		Joins("JOIN bank_accounts ON withdrawals.bank_account_id = bank_accounts.id").
		Joins("JOIN banks ON bank_accounts.bank_id = banks.id").
		Where("users.user_mode != ? OR users.user_mode IS NULL", "promotor")

	// Apply filters
	if status != "" {
		query = query.Where("withdrawals.status = ?", status)
	}
	if userID != "" {
		query = query.Where("withdrawals.user_id = ?", userID)
	}
	if orderID != "" {
		query = query.Where("withdrawals.order_id LIKE ?", "%"+orderID+"%")
	}

	// Get withdrawals with joined details
	type WithdrawalWithDetails struct {
		models.Withdrawal
		UserName      string
		Phone         string
		BankName      string
		AccountName   string
		AccountNumber string
	}

	var withdrawals []WithdrawalWithDetails
	query.Select("withdrawals.*, users.name as user_name, users.number as phone, banks.name as bank_name, bank_accounts.account_name, bank_accounts.account_number").
		Offset(offset).
		Limit(limit).
		Order("withdrawals.created_at DESC").
		Find(&withdrawals)

	// Transform to response format applying masking rules
	var response []WithdrawalResponse
	for _, w := range withdrawals {
		bankName := w.BankName
		accountName := w.AccountName
		accountNumber := w.AccountNumber
		response = append(response, WithdrawalResponse{
			ID:            w.ID,
			UserID:        w.UserID,
			UserName:      w.UserName,
			Phone:         w.Phone,
			BankAccountID: w.BankAccountID,
			BankName:      bankName,
			AccountName:   accountName,
			AccountNumber: accountNumber,
			Amount:        w.Amount,
			Charge:        w.Charge,
			FinalAmount:   w.FinalAmount,
			OrderID:       w.OrderID,
			Status:        w.Status,
			CreatedAt:     w.CreatedAt.Format(time.RFC3339),
		})
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data:    response,
	})
}

func ApproveWithdrawal(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 30 * time.Second}
	vars := mux.Vars(r)
	id, err := strconv.ParseUint(vars["id"], 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "ID penarikan tidak valid",
		})
		return
	}

	var withdrawal models.Withdrawal
	if err := database.DB.First(&withdrawal, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Penarikan tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data penarikan",
		})
		return
	}

	if withdrawal.Status != "Pending" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Hanya penarikan dengan status Pending yang dapat disetujui",
		})
		return
	}

	var setting models.Setting
	if err := database.DB.First(&setting).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil informasi aplikasi",
		})
		return
	}

	// Check auto_withdraw setting
	if !setting.AutoWithdraw {
		tx := database.DB.Begin()

		withdrawal.Status = "Success"
		if err := tx.Save(&withdrawal).Error; err != nil {
			tx.Rollback()
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: "Gagal memperbarui status penarikan",
			})
			return
		}

		if err := tx.Model(&models.Transaction{}).Where("order_id = ?", withdrawal.OrderID).Update("status", "Success").Error; err != nil {
			tx.Rollback()
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal memperbarui status transaksi"})
			return
		}

		if err := tx.Commit().Error; err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal menyimpan perubahan"})
			return
		}

		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Penarikan berhasil disetujui (transfer manual)"})
		return
	}

	// Auto withdrawal using Pakailink
	var ba models.BankAccount
	if err := database.DB.Preload("Bank").First(&ba, withdrawal.BankAccountID).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengambil rekening"})
		return
	}

	bank := ba.Bank
	if bank == nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Data bank tidak lengkap"})
		return
	}

	accessToken, err := utils.GetPakailinkAccessToken(r.Context(), client)
	if err != nil {
		log.Printf("[Pakailink] GetPakailinkAccessToken error: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan saat memanggil layanan pembayaran",
		})
		return
	}

	callbackURL := utils.GetPakailinkPayoutCallbackURL()
	amount := int64(withdrawal.FinalAmount)
	partnerRefNo := withdrawal.OrderID

	bankType := strings.TrimSpace(bank.Type)
	if bankType == "" {
		bankType = "bank"
	}
	payoutCode := bank.Code

	// Call Pakailink: bank transfer or ewallet topup
	if strings.ToLower(bankType) == "ewallet" {
		_, err = utils.PakailinkEwalletTopup(r.Context(), client, accessToken, partnerRefNo, ba.AccountNumber, payoutCode, "", amount, callbackURL)
	} else {
		_, err = utils.PakailinkBankTransfer(r.Context(), client, accessToken, partnerRefNo, ba.AccountNumber, payoutCode, "", amount, callbackURL)
	}

	if err != nil {
		log.Printf("[Pakailink] Payout error: %v", err)
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Terjadi kesalahan saat memproses transfer",
		})
		return
	}

	// Pakailink returns 2004300/2003800 = request accepted, status Pending. Callback akan update ke Success/Failed.
	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Permintaan transfer telah dikirim. Status akan diperbarui via callback.",
		Data: map[string]interface{}{
			"order_id": withdrawal.OrderID,
			"status":   "Pending",
		},
	})
}

func RejectWithdrawal(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseUint(vars["id"], 10, 32)
	if err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "ID penarikan tidak valid",
		})
		return
	}

	var withdrawal models.Withdrawal
	if err := database.DB.First(&withdrawal, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Penarikan tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data penarikan",
		})
		return
	}

	// Only allow rejecting pending withdrawals
	if withdrawal.Status != "Pending" {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Hanya penarikan dengan status Pending yang dapat ditolak",
		})
		return
	}

	// Start transaction
	tx := database.DB.Begin()

	// Update withdrawal status
	withdrawal.Status = "Failed"
	if err := tx.Save(&withdrawal).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status penarikan",
		})
		return
	}

	// Update related transaction status
	if err := tx.Model(&models.Transaction{}).
		Where("order_id = ?", withdrawal.OrderID).
		Update("status", "Failed").Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status transaksi",
		})
		return
	}

	// Refund the amount to user's balance
	var user models.User
	if err := tx.First(&user, withdrawal.UserID).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data pengguna",
		})
		return
	}

	user.Balance += withdrawal.Amount
	if err := tx.Save(&user).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui saldo pengguna",
		})
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal menyimpan perubahan",
		})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Penarikan berhasil ditolak",
		Data: map[string]interface{}{
			"id":     withdrawal.ID,
			"status": withdrawal.Status,
		},
	})
}

// PakailinkPayoutCallbackPayload from docs/payout.md
type PakailinkPayoutCallbackPayload struct {
	TransactionData *struct {
		PaymentFlagStatus  string `json:"paymentFlagStatus"` // 00=Success, 03=Pending, 06=Failed
		PartnerReferenceNo string `json:"partnerReferenceNo"`
		AccountNumber      string `json:"accountNumber"`
		AccountName        string `json:"accountName"`
		ReferenceNo        string `json:"referenceNo"`
	} `json:"transactionData"`
}

// PakailinkPayoutCallbackHandler handles Pakailink payout callback (bank transfer & ewallet topup)
// POST /v3/callback/payouts
func PakailinkPayoutCallbackHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var payload PakailinkPayoutCallbackPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Invalid JSON"})
		return
	}

	writePakailinkPayoutSuccess := func() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"responseCode":"2004400","responseMessage":"Successful"}`))
	}

	if payload.TransactionData == nil {
		writePakailinkPayoutSuccess()
		return
	}

	referenceID := strings.TrimSpace(payload.TransactionData.PartnerReferenceNo)
	statusCode := strings.TrimSpace(payload.TransactionData.PaymentFlagStatus)
	var status string
	switch statusCode {
	case "00":
		status = "Success"
	case "06":
		status = "Failed"
	default:
		writePakailinkPayoutSuccess()
		return
	}

	if referenceID == "" {
		writePakailinkPayoutSuccess()
		return
	}

	db := database.DB
	var withdrawal models.Withdrawal
	if err := db.Where("order_id = ?", referenceID).First(&withdrawal).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{
				Success: false,
				Message: "Penarikan tidak ditemukan",
			})
			return
		}
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal mengambil data penarikan",
		})
		return
	}

	// Start transaction
	tx := db.Begin()

	// Process based on status
	if status == "Success" {
		// Update withdrawal and transaction status to Success (idempotent - can be called multiple times)
		withdrawal.Status = "Success"
		if err := tx.Save(&withdrawal).Error; err != nil {
			tx.Rollback()
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: "Gagal memperbarui status penarikan",
			})
			return
		}

		// Update related transaction status to Success
		if err := tx.Model(&models.Transaction{}).
			Where("order_id = ?", withdrawal.OrderID).
			Update("status", "Success").Error; err != nil {
			tx.Rollback()
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: "Gagal memperbarui status transaksi",
			})
			return
		}

		if err := tx.Commit().Error; err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
				Success: false,
				Message: "Gagal menyimpan perubahan",
			})
			return
		}

		writePakailinkPayoutSuccess()
		return
	}

	// statusCode 03 or other = Pending, return success without updating
	if statusCode != "06" {
		writePakailinkPayoutSuccess()
		return
	}

	// status = Failed (06): update to Pending for admin retry

	// If status is Failed, update withdrawal status to Pending (for admin to retry)
	withdrawal.Status = "Pending"
	if err := tx.Save(&withdrawal).Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status penarikan",
		})
		return
	}

	if err := tx.Model(&models.Transaction{}).
		Where("order_id = ?", withdrawal.OrderID).
		Update("status", "Pending").Error; err != nil {
		tx.Rollback()
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal memperbarui status transaksi",
		})
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{
			Success: false,
			Message: "Gagal menyimpan perubahan",
		})
		return
	}

	writePakailinkPayoutSuccess()
}
