package controllers

import (
	"net/http"
	"strings"

	"project/database"
	"project/models"
	"project/utils"
)

func BankListHandler(w http.ResponseWriter, r *http.Request) {
	db := database.DB
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))

	query := db.Where("status = ?", "Active")
	if typeFilter != "" {
		query = query.Where("type = ?", strings.ToLower(typeFilter))
	}
	if search != "" {
		s := "%" + search + "%"
		query = query.Where("name LIKE ? OR short_name LIKE ? OR code LIKE ?", s, s, s)
	}

	var banks []models.Bank
	if err := query.Order("CASE WHEN type='ewallet' THEN 0 ELSE 1 END, name ASC").Find(&banks).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Terjadi kesalahan sistem, silakan coba lagi"})
		return
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Successfully",
		Data: map[string]interface{}{
			"banks": banks,
		},
	})
}
