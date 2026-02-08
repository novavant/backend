package telegram

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"project/database"
	"project/models"
	"project/utils"
)

// ConversationHistory stores the last 10 messages per user
type ConversationHistory struct {
	Messages []utils.GroqMessage
	LastSeen time.Time
}

// RateLimiter tracks user rate limits
type RateLimiter struct {
	mu       sync.RWMutex
	lastCall map[int64]time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		lastCall: make(map[int64]time.Time),
	}
}

func (rl *RateLimiter) CanProceed(userID int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	lastCall, exists := rl.lastCall[userID]
	if !exists {
		rl.lastCall[userID] = time.Now()
		return true
	}

	if time.Since(lastCall) < 5*time.Second {
		return false
	}

	rl.lastCall[userID] = time.Now()
	return true
}

// TelegramUpdate represents a Telegram webhook update
type TelegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		From      *struct {
			ID        int64  `json:"id"`
			IsBot     bool   `json:"is_bot"`
			FirstName string `json:"first_name"`
			Username  string `json:"username"`
		} `json:"from"`
		Chat *struct {
			ID    int64  `json:"id"`
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"chat"`
		Text     string `json:"text"`
		Entities []struct {
			Type   string `json:"type"`
			Offset int    `json:"offset"`
			Length int    `json:"length"`
		} `json:"entities"`
		ReplyToMessage *struct {
			MessageID int64 `json:"message_id"`
			From      *struct {
				ID        int64  `json:"id"`
				IsBot     bool   `json:"is_bot"`
				FirstName string `json:"first_name"`
			} `json:"from"`
			Text string `json:"text"`
		} `json:"reply_to_message"`
	} `json:"message"`
}

// TelegramMessage represents a message to send
type TelegramMessage struct {
	ChatID      int64  `json:"chat_id"`
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode,omitempty"`
	ReplyToID   int64  `json:"reply_to_message_id,omitempty"`
	ReplyMarkup *struct {
		ForceReply bool `json:"force_reply"`
	} `json:"reply_markup,omitempty"`
}

var (
	conversationHistory = make(map[int64]*ConversationHistory)
	historyMutex        sync.RWMutex
	rateLimiter         = NewRateLimiter()
	allowedGroupIDs     []int64
)

func init() {
	// Load allowed group IDs from environment
	groupIDsStr := os.Getenv("TELEGRAM_ALLOWED_GROUP_IDS")
	if groupIDsStr != "" {
		groupIDs := strings.Split(groupIDsStr, ",")
		for _, idStr := range groupIDs {
			var id int64
			if _, err := fmt.Sscanf(strings.TrimSpace(idStr), "%d", &id); err == nil {
				allowedGroupIDs = append(allowedGroupIDs, id)
			}
		}
	}
}

// SendTelegramMessage sends a message via Telegram Bot API
func SendTelegramMessage(chatID int64, text string, replyToID int64) error {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN not set")
	}

	msg := TelegramMessage{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "HTML",
	}
	if replyToID > 0 {
		msg.ReplyToID = replyToID
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}

	return nil
}

// ShouldRespond checks if bot should respond to the message
func ShouldRespond(update *TelegramUpdate) bool {
	if update.Message == nil {
		return false
	}

	// Ignore messages from bots
	if update.Message.From != nil && update.Message.From.IsBot {
		return false
	}

	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return false
	}

	// Ignore commands (messages starting with /)
	if strings.HasPrefix(text, "/") {
		return false
	}

	chatType := update.Message.Chat.Type

	// For private chats, always respond (it's definitely for the bot)
	if chatType == "private" {
		return true
	}

	// For groups, only respond if message is clearly directed to the bot
	if chatType == "group" || chatType == "supergroup" {
		// Check if group is allowed
		if len(allowedGroupIDs) > 0 {
			allowed := false
			for _, id := range allowedGroupIDs {
				if update.Message.Chat.ID == id {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}

		// Check if message is directed to the bot
		return isMessageForBot(update, text)
	}

	// Ignore other chat types (channel, etc.)
	return false
}

// isMessageForBot checks if a message in a group is clearly directed to the bot
func isMessageForBot(update *TelegramUpdate, text string) bool {
	textLower := strings.ToLower(text)

	// Check if bot is mentioned
	botUsername := os.Getenv("TELEGRAM_BOT_USERNAME")
	if botUsername != "" {
		botUsername = strings.ToLower(strings.TrimPrefix(botUsername, "@"))
		if strings.Contains(textLower, "@"+botUsername) {
			return true
		}
	}

	// Check if message is a reply to bot's message
	if update.Message.ReplyToMessage != nil {
		if update.Message.ReplyToMessage.From != nil && update.Message.ReplyToMessage.From.IsBot {
			return true
		}
	}

	// Check for explicit bot mentions or requests
	botKeywords := []string{
		"bot", "cs", "admin", "min", "customer service",
		"bantuan", "help", "tolong", "minta tolong",
		"bisa bantu", "bisa tolong", "mau tanya",
	}

	for _, keyword := range botKeywords {
		if strings.Contains(textLower, keyword) {
			return true
		}
	}

	// Check if message is a question (with or without question mark)
	// Detect question words and Nova Vant-related keywords
	novavantKeywords := []string{
		"nova vant", "novavant", "produk", "harga", "profit", "penarikan", "withdraw",
		"investasi", "neura", "finora", "corex", "daftar", "beli",
		"deposit", "komisi", "referral", "vip", "level", "task", "tugas",
		"spin", "hadiah", "event", "berita", "news", "forum", "bank",
		"rekening", "saldo", "bonus", "kontrak", "durasi", "lisensi",
		"publisher", "lupa password", "forgot password",
	}

	// Check if it's a question (has question mark OR question words)
	isQuestion := strings.Contains(text, "?") ||
		strings.Contains(textLower, "cara ") ||
		strings.Contains(textLower, "bagaimana ") ||
		strings.Contains(textLower, "kenapa ") ||
		strings.Contains(textLower, "mengapa ") ||
		strings.Contains(textLower, "gimana ") ||
		strings.Contains(textLower, "apa ") ||
		strings.Contains(textLower, "kapan ") ||
		strings.Contains(textLower, "dimana ") ||
		strings.Contains(textLower, "berapa ") ||
		strings.Contains(textLower, "bisa ") ||
		strings.Contains(textLower, "boleh ")

	if isQuestion && len(text) < 300 {
		// Check if it's about Nova Vant
		for _, keyword := range novavantKeywords {
			if strings.Contains(textLower, keyword) {
				return true
			}
		}
	}

	// Check conversation history - if bot asked a question recently, this might be an answer
	history := GetConversationHistory(update.Message.From.ID)
	if len(history) > 0 {
		// Check if last message from bot was a question
		lastMessage := history[len(history)-1]
		if lastMessage.Role == "assistant" {
			lastContent := strings.ToLower(lastMessage.Content)
			// Check if last bot message contains a question
			if strings.Contains(lastContent, "?") ||
				strings.Contains(lastContent, "kamu baru") ||
				strings.Contains(lastContent, "sudah pernah") ||
				strings.Contains(lastContent, "level vip") ||
				strings.Contains(lastContent, "baru di nova vant") {
				// This is likely an answer to bot's question
				return true
			}
		}
	}

	// If none of the above, it's probably just casual chat between members
	// Don't respond
	return false
}

// GetConversationHistory returns the last 20 messages for a user
func GetConversationHistory(userID int64) []utils.GroqMessage {
	historyMutex.RLock()
	defer historyMutex.RUnlock()

	history, exists := conversationHistory[userID]
	if !exists {
		return []utils.GroqMessage{}
	}

	// Return last 20 messages for better context
	if len(history.Messages) > 20 {
		return history.Messages[len(history.Messages)-20:]
	}
	return history.Messages
}

// AddToConversationHistory adds a message to conversation history
func AddToConversationHistory(userID int64, role string, content string) {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	if conversationHistory[userID] == nil {
		conversationHistory[userID] = &ConversationHistory{
			Messages: []utils.GroqMessage{},
			LastSeen: time.Now(),
		}
	}

	conversationHistory[userID].Messages = append(conversationHistory[userID].Messages, utils.GroqMessage{
		Role:    role,
		Content: content,
	})

	// Keep only last 20 messages for better context
	if len(conversationHistory[userID].Messages) > 20 {
		conversationHistory[userID].Messages = conversationHistory[userID].Messages[len(conversationHistory[userID].Messages)-20:]
	}

	conversationHistory[userID].LastSeen = time.Now()
}

// DetectFAQType detects what type of information is being asked
func DetectFAQType(question string) string {
	question = strings.ToLower(question)

	if strings.Contains(question, "harga") || strings.Contains(question, "price") || strings.Contains(question, "berapa") {
		return "prices"
	}
	if strings.Contains(question, "produk") || strings.Contains(question, "product") || strings.Contains(question, "neura") {
		return "products"
	}
	if strings.Contains(question, "minimal penarikan") || strings.Contains(question, "min penarikan") || strings.Contains(question, "minimal withdraw") {
		return "withdrawal_info"
	}
	if strings.Contains(question, "waktu penarikan") || strings.Contains(question, "jam penarikan") || strings.Contains(question, "withdrawal time") {
		return "withdrawal_time"
	}
	if strings.Contains(question, "cara daftar") || strings.Contains(question, "cara mendaftar") || strings.Contains(question, "register") || strings.Contains(question, "pendaftaran") {
		return "registration"
	}
	if strings.Contains(question, "cara penarikan") || strings.Contains(question, "cara withdraw") || strings.Contains(question, "withdraw") {
		return "withdrawal_guide"
	}
	if strings.Contains(question, "cara beli") || strings.Contains(question, "cara pembelian") || strings.Contains(question, "beli produk") || strings.Contains(question, "pembelian") {
		return "purchase"
	}
	if strings.Contains(question, "profit tidak masuk") || strings.Contains(question, "profit gak masuk") || strings.Contains(question, "profit belum masuk") ||
		strings.Contains(question, "kenapa profit") || strings.Contains(question, "mengapa profit") || strings.Contains(question, "profit kok") ||
		strings.Contains(question, "profit locked") || strings.Contains(question, "profit terkunci") || strings.Contains(question, "profit terlock") {
		return "profit_neura"
	}
	if strings.Contains(question, "deposit") || strings.Contains(question, "minimal deposit") {
		return "deposit"
	}
	if strings.Contains(question, "komisi") || strings.Contains(question, "referral") || strings.Contains(question, "undang") {
		return "commission"
	}
	if strings.Contains(question, "vip") || strings.Contains(question, "level") {
		return "vip"
	}
	if strings.Contains(question, "event") || strings.Contains(question, "tiktok") || strings.Contains(question, "youtube") || strings.Contains(question, "upload") {
		return "event"
	}
	if strings.Contains(question, "berita") || strings.Contains(question, "news") || strings.Contains(question, "artikel") {
		return "news"
	}
	if strings.Contains(question, "task") || strings.Contains(question, "tugas") {
		return "task"
	}
	if strings.Contains(question, "spin") || strings.Contains(question, "hadiah") || strings.Contains(question, "prize") {
		return "spin"
	}
	if strings.Contains(question, "forum") || strings.Contains(question, "bukti penarikan") {
		return "forum"
	}
	if strings.Contains(question, "bank") || strings.Contains(question, "rekening") {
		return "bank"
	}
	if strings.Contains(question, "tentang nova vant") || strings.Contains(question, "about") || strings.Contains(question, "apa itu nova vant") {
		return "about"
	}
	if strings.Contains(question, "lisensi") || strings.Contains(question, "legal") || strings.Contains(question, "sertifikat") {
		return "license"
	}
	if strings.Contains(question, "publisher") || strings.Contains(question, "menjadi publisher") {
		return "publisher"
	}
	if strings.Contains(question, "lupa password") || strings.Contains(question, "forgot password") || strings.Contains(question, "reset password") {
		return "forgot_password"
	}
	if strings.Contains(question, "jam berapa") || strings.Contains(question, "waktu sekarang") || strings.Contains(question, "jam sekarang") || strings.Contains(question, "pukul berapa") {
		return "current_time"
	}
	if strings.Contains(question, "pagi") || strings.Contains(question, "siang") || strings.Contains(question, "sore") || strings.Contains(question, "malam") ||
		strings.Contains(question, "halo") || strings.Contains(question, "hai") || strings.Contains(question, "hi") || strings.Contains(question, "hello") {
		// Check if it's just a greeting (not a question about greeting)
		if !strings.Contains(question, "?") && !strings.Contains(question, "apa") && !strings.Contains(question, "bagaimana") {
			return "greeting"
		}
	}

	return ""
}

// getCurrentTimeContext returns current time in WIB format with date
func getCurrentTimeContext() string {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)

	// Format: "Waktu saat ini: 14:35 WIB (Senin, 30 Desember 2025)"
	dayNames := []string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	monthNames := []string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}

	dayName := dayNames[now.Weekday()]
	monthName := monthNames[int(now.Month())]

	return fmt.Sprintf("Waktu saat ini: %s WIB (%s, %d %s %d)",
		now.Format("15:04"),
		dayName,
		now.Day(),
		monthName,
		now.Year())
}

// GetContextData retrieves relevant data from database based on FAQ type
func GetContextData(faqType string) string {
	// Add current time context to all responses
	timeContext := getCurrentTimeContext()

	switch faqType {
	case "prices", "products":
		return getProductDataForAI() + "\n\n" + timeContext
	case "withdrawal_info":
		return getWithdrawalInfoForAI() + "\n\n" + timeContext
	case "withdrawal_time":
		return "Waktu penarikan: Senin-Sabtu, pukul 09:00-17:00 WIB. Penarikan di luar jam tersebut tidak dapat diproses.\n\n" + timeContext
	case "registration":
		return "Cara mendaftar: 1) Akses https://novavant.com/register, 2) Isi data diri (nama, nomor telepon, password minimal 6 karakter, kode referral), 3) Klik Daftar. Setelah mendaftar, member akan mendapat bonus pendaftaran Rp2.000.\n\n" + timeContext
	case "withdrawal_guide":
		return "Cara penarikan: 1) Pastikan saldo mencukupi dan waktu penarikan (Senin-Sabtu, 09:00-17:00 WIB), 2) Buka menu Penarikan, 3) Tambahkan rekening bank jika belum ada, 4) Masukkan jumlah yang ingin ditarik, 5) Pilih rekening tujuan, 6) Konfirmasi. Penarikan hanya dapat dilakukan 1 kali per hari.\n\n" + timeContext
	case "purchase":
		return "Cara membeli produk: 1) Buka aplikasi Nova Vant, 2) Pilih menu Produk/Investasi, 3) Pilih produk yang ingin dibeli, 4) Baca detail produk (harga, profit, durasi), 5) Pilih metode pembayaran, 6) Klik Konfirmasi, 7) Lakukan pembayaran sesuai instruksi. Setelah pembayaran berhasil, produk akan otomatis berjalan sesuai durasi.\n\n" + timeContext
	case "profit_neura":
		return getProfitNeuraInfo() + "\n\n" + timeContext
	case "deposit":
		return `PENTING: Nova Vant TIDAK memiliki menu deposit terpisah!

Cara "deposit" di Nova Vant:
- Saat Anda melakukan investasi produk, pembayaran dilakukan langsung melalui QRIS/Virtual Account
- Tidak ada menu deposit terpisah - langsung investasi dan bayar
- Tidak ada biaya deposit tambahan
- Minimal investasi sesuai dengan produk yang dipilih

SISTEM VIP & PRODUK (PENTING UNTUK SARAN PRODUK):
- User BARU (VIP 0): Hanya bisa membeli produk ROUTER. Semua produk Neura bisa dibeli dari VIP 0 (tidak ada requirement VIP untuk Neura)
- Setelah investasi Neura, level VIP otomatis naik sesuai total investasi Neura
- Setelah level VIP naik, baru bisa membeli produk Finora sesuai level VIP yang dicapai
- Finora & Corex memerlukan level VIP tertentu (tidak bisa dibeli dari VIP 0)
- Neura TIDAK memerlukan VIP level - semua Neura bisa dibeli dari VIP 0

JIKA USER BERTANYA TENTANG DEPOSIT ATAU MINIMAL DEPOSIT ATAU PRODUK YANG HARUS DIAMBIL:
1. TANYA DULU apakah user baru atau sudah pernah investasi di Nova Vant
2. Jika user BARU (VIP 0): Sarankan produk ROUTER saja (semua Neura bisa dibeli dari VIP 0)
3. Jika user sudah pernah investasi Neura: Tanyakan level VIP mereka, lalu sarankan produk sesuai level VIP
4. JANGAN langsung sarankan Finora/Corex tanpa tahu level VIP user
5. Deposit = Investasi langsung dengan pembayaran QRIS/VA

` + timeContext

	case "commission":
		return getCommissionInfo() + "\n\n" + timeContext
	case "vip":
		return getVIPInfo() + "\n\n" + timeContext
	case "event":
		return getEventInfo() + "\n\n" + timeContext
	case "news":
		return getNewsInfo() + "\n\n" + timeContext
	case "task":
		return getTaskInfo() + "\n\n" + timeContext
	case "spin":
		return getSpinInfo() + "\n\n" + timeContext
	case "forum":
		return "Nova Vant memiliki halaman forum bukti penarikan di https://novavant.com/forum untuk melihat semua bukti user lain melakukan penarikan. Di sini Anda bisa melihat testimoni terverifikasi dari member yang sudah melakukan penarikan.\n\n" + timeContext
	case "bank":
		return "Maksimal akun bank yang bisa ditambahkan adalah 3 rekening. Jika sudah 3 rekening, tidak bisa ditambah lagi. Untuk menambah rekening, akses https://novavant.com/bank/add\n\n" + timeContext
	case "about":
		return getAboutNovaVant() + "\n\n" + timeContext
	case "license":
		return getLicenseInfo() + "\n\n" + timeContext
	case "publisher":
		return "User bisa menjadi publisher news/artikel di Nova Vant. Setiap menambahkan news akan diberikan hadiah berupa saldo Nova Vant. Cara mendaftar menjadi publisher: hubungi CS dengan tag @novavant_cs. Situs publisher: https://news.novavant.com/publisher/login\n\n" + timeContext
	case "forgot_password":
		return "Cara reset password: 1) Akses https://novavant.com/forgot-password, 2) Masukkan nomor yang terdaftar di Nova Vant, 3) Masukkan kode OTP yang dikirim ke WhatsApp, 4) Ganti kata sandi baru. Simple dan mudah!\n\n" + timeContext
	case "current_time":
		return getCurrentTimeWIB()
	case "greeting":
		return getGreetingResponse()
	default:
		return timeContext
	}
}

// getProductDataForAI returns product data formatted for AI context
func getProductDataForAI() string {
	db := database.DB
	var products []models.Product
	if err := db.Where("status = ?", "Active").Preload("Category").Order("category_id ASC, id ASC").Find(&products).Error; err != nil {
		return "Tidak dapat mengakses data produk saat ini."
	}

	if len(products) == 0 {
		return "Belum ada produk yang tersedia."
	}

	var response strings.Builder
	response.WriteString("<b>Daftar Produk Nova Vant</b> 📦\n\n")

	// Group by category
	categoryMap := make(map[string][]models.Product)
	for _, product := range products {
		categoryName := "Umum"
		if product.Category != nil {
			categoryName = product.Category.Name
		}
		categoryMap[categoryName] = append(categoryMap[categoryName], product)
	}

	for categoryName, prods := range categoryMap {
		response.WriteString(fmt.Sprintf("<b>Kategori: %s</b>\n", categoryName))
		for _, product := range prods {
			response.WriteString(fmt.Sprintf("- <b>%s</b> - Harga Rp%.0f, Profit Harian Rp%.0f, Durasi %d hari",
				product.Name, product.Amount, product.DailyProfit, product.Duration))
			// Neura tidak memerlukan VIP level (bisa dibeli dari VIP 0)
			if categoryName != "Neura" && product.RequiredVIP > 0 {
				response.WriteString(fmt.Sprintf(", VIP Level %d", product.RequiredVIP))
			} else if categoryName == "Neura" {
				response.WriteString(" (VIP 0 bisa beli)")
			}
			if product.PurchaseLimit > 0 {
				response.WriteString(fmt.Sprintf(", Batas Pembelian %d kali", product.PurchaseLimit))
			}
			response.WriteString("\n")
		}
		response.WriteString("\n")
	}

	// Add Neura information
	response.WriteString("<b>Info Penting Produk Neura</b> 📦\n")
	response.WriteString("Produk Neura akan diterima oleh member SETELAH KONTRAK BERAKHIR. Profit harian akan tetap berjalan sesuai durasi kontrak, dan produk fisik akan dikirim setelah kontrak selesai.\n")
	response.WriteString("\n<b>Sistem Profit Neura (Penting!)</b> ⚠️💰\n")
	response.WriteString("- Untuk produk ROUTER, profit TIDAK masuk setiap hari (profit terkunci/locked)\n")
	response.WriteString("- Profit akan dikembalikan FULL selama durasi kontrak (70 hari) BESERTA modal setelah kontrak 70 hari selesai\n")
	response.WriteString("- Jadi jika member membeli Neura dan profit tidak masuk, itu NORMAL karena profit Neura terkunci dan akan dikembalikan sekaligus setelah kontrak selesai\n")
	response.WriteString("\n<b>VIP Level untuk Neura (Penting!)</b> ⭐\n")
	response.WriteString("- Semua produk NEURA bisa dibeli dari VIP 0 (TIDAK ada requirement VIP untuk Neura)\n")
	response.WriteString("- Neura TIDAK memerlukan VIP level tertentu - semua Neura bisa dibeli oleh user baru\n")
	response.WriteString("- Setelah investasi Neura, level VIP otomatis naik sesuai total investasi Neura\n")

	return response.String()
}

// getProfitNeuraInfo returns information about neura profit system
func getProfitNeuraInfo() string {
	return `<b>Informasi Penting Tentang Profit Neura</b> ⚠️💰

<b>Pertanyaan yang Sering Ditanyakan:</b> "Kenapa profit saya tidak masuk?"

<b>Jawaban:</b>
Jika Anda membeli produk ROUTER, profit TIDAK akan masuk setiap hari karena profit neura TERKUNCI (LOCKED).

<b>Sistem Profit Neura:</b>
- Profit neura TIDAK masuk setiap hari (profit terkunci/locked)
- Profit akan dikembalikan FULL selama durasi kontrak (70 hari) BESERTA modal setelah kontrak 70 hari selesai
- Jadi total yang akan diterima = Modal + (Profit Harian × 70 hari)
- Neura fisik juga akan dikirim setelah kontrak selesai

<b>Jadi jika member bertanya "Kenapa profit saya tidak masuk?":</b>
- Cek dulu apakah mereka membeli produk ROUTER
- Jika iya, jelaskan bahwa itu NORMAL karena profit neura terkunci
- Profit akan dikembalikan sekaligus setelah kontrak 70 hari selesai beserta modal
- Neura fisik juga akan dikirim setelah kontrak selesai

<b>Catatan:</b> Hanya produk ROUTER yang memiliki sistem profit terkunci. Produk lain profit masuk setiap hari seperti biasa.`
}

// getCommissionInfo returns information about referral commission
func getCommissionInfo() string {
	return `<b>Sistem Komisi Referral Nova Vant</b> 💰

<b>Komisi Instan:</b>
- Dapatkan 30% komisi langsung saat referral Anda melakukan investasi
- Contoh: Jika referral investasi Rp100.000, Anda dapat komisi Rp30.000

<b>Unlimited Earning:</b>
- Tidak ada batas maksimal penghasilan dari program referral
- Semakin banyak referral yang invest, semakin besar komisi yang didapat

<b>Easy Start:</b>
- Cukup bagikan kode atau link referral
- Tidak perlu investasi tambahan untuk mulai mendapatkan komisi

<b>Cara menggunakan:</b>
- Akses <a href="https://novavant.com/referral">https://novavant.com/referral</a> untuk melihat kode referral dan link
- Bagikan kode atau link ke teman
- Setelah teman investasi, komisi langsung masuk ke saldo Anda`
}

// getVIPInfo returns information about VIP levels
func getVIPInfo() string {
	return `<b>Level VIP Nova Vant</b> ⭐

<b>VIP 0 (Basic)</b> - Saat Ini:
- Akses produk Neura
- Investasi dengan aman
- Investasi tanpa batas

<b>VIP 1 (Bronze)</b> - Target: Rp 50.000:
- Semua benefit VIP 0
- Membuka Finora 1
- Profit hingga 140%

<b>VIP 2 (Silver)</b> - Target: Rp 1.200.000:
- Semua benefit VIP 1
- Membuka Finora 2
- Profit hingga 210%

<b>VIP 3 (Gold)</b> - Target: Rp 7.000.000:
- Semua benefit VIP 2
- Membuka Finora 3
- Membuka semua produk Corex
- Profit hingga 235%

<b>VIP 4 (Platinum)</b> - Target: Rp 30.000.000:
- Semua benefit VIP 3
- Membuka Finora 4
- Profit hingga 280%

<b>VIP 5 (Ultimate)</b> - Target: Rp 150.000.000:
- Semua benefit VIP 4
- Semua produk tersedia

<b>Cara Naik Level VIP:</b>
- Investasi pada produk Neura menaikkan level VIP
- Produk Neura dengan profit terkunci yang menaikkan level VIP
- Finora & Corex TIDAK menaikkan level VIP (profit langsung)

💡 <b>Tips:</b> Investasi Neura memberikan return total saat selesai dan menaikkan level VIP. Semakin tinggi level, semakin banyak produk eksklusif!`
}

// getEventInfo returns information about social media event
func getEventInfo() string {
	return `<b>Event Upload Sosmed Nova Vant</b> 🎬

<b>Raih Hadiah Fantastis!</b>

Buat konten promosi Nova Vant di TikTok & YouTube, raih views, dan claim hadiahnya!

<b>Hadiah:</b>
- 20K views = Rp 100.000
- 50K views = Rp 300.000
- 100K views = Rp 700.000
- 250K views = Rp 1.000.000
- 500K views = Rp 2.000.000

<b>Syarat & Ketentuan:</b>
- Video original berkualitas HD, tanpa re-upload
- Dilarang menggunakan BOT atau fake views
- Wajib mencantumkan link referral di bio/deskripsi
- Hadiah akan ditambahkan langsung ke saldo akun

<b>Cara Mengajukan Claim Hadiah:</b>
Chat CS dengan tag @novavant_cs untuk mengajukan claim hadiah setelah mencapai target views`
}

// getNewsInfo fetches and returns news from API
func getNewsInfo() string {
	resp, err := http.Get("https://api-news.novavant.com/v1/novavant/newest")
	if err != nil {
		return "Tidak dapat mengakses berita saat ini. Silakan kunjungi https://news.novavant.com untuk melihat berita terbaru."
	}
	defer resp.Body.Close()

	var newsResponse struct {
		Success bool `json:"success"`
		Data    []struct {
			Title     string `json:"title"`
			Excerpt   string `json:"excerpt"`
			Thumbnail string `json:"thumbnail"`
			Href      string `json:"href"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&newsResponse); err != nil {
		return "Tidak dapat mengakses berita saat ini. Silakan kunjungi https://news.novavant.com untuk melihat berita terbaru."
	}

	if len(newsResponse.Data) == 0 {
		return "Belum ada berita tersedia. Silakan kunjungi https://news.novavant.com untuk melihat berita terbaru."
	}

	var response strings.Builder
	response.WriteString("<b>Berita Terbaru Nova Vant (Top 3)</b> 📰\n\n")

	// Show top 3 news
	maxNews := 3
	if len(newsResponse.Data) < maxNews {
		maxNews = len(newsResponse.Data)
	}

	for i := 0; i < maxNews; i++ {
		news := newsResponse.Data[i]
		response.WriteString(fmt.Sprintf("<b>%d. %s</b>\n", i+1, news.Title))
		response.WriteString(fmt.Sprintf("%s\n", news.Excerpt))
		response.WriteString(fmt.Sprintf("<a href=\"%s\">Baca selengkapnya</a>\n\n", news.Href))
	}

	response.WriteString("Untuk berita lainnya, kunjungi: <a href=\"https://news.novavant.com\">https://news.novavant.com</a>")

	return response.String()
}

// getTaskInfo returns information about tasks
func getTaskInfo() string {
	db := database.DB
	var tasks []models.Task
	if err := db.Where("status = ?", "Active").Order("required_level ASC").Find(&tasks).Error; err != nil {
		return "Tidak dapat mengakses daftar tugas saat ini. Akses https://novavant.com/referral untuk melihat tugas yang tersedia."
	}

	if len(tasks) == 0 {
		return "Belum ada tugas tersedia. Akses https://novavant.com/referral untuk melihat tugas yang tersedia."
	}

	var response strings.Builder
	response.WriteString("<b>Daftar Tugas Nova Vant</b> 📋\n\n")

	for _, task := range tasks {
		response.WriteString(fmt.Sprintf("- <b>%s</b>\n", task.Name))
		response.WriteString(fmt.Sprintf("  Hadiah: Rp%.0f\n", task.Reward))
		response.WriteString(fmt.Sprintf("  Level Diperlukan: %d\n", task.RequiredLevel))
		response.WriteString(fmt.Sprintf("  Member Aktif Diperlukan: %d\n\n", task.RequiredActiveMembers))
	}

	response.WriteString("Akses <a href=\"https://novavant.com/referral\">https://novavant.com/referral</a> untuk melihat detail dan claim tugas.")

	return response.String()
}

// getSpinInfo returns information about spin wheel
func getSpinInfo() string {
	return `<b>Spin Wheel Berhadiah Nova Vant</b> 🎰

<b>Cara Dapat Tiket Spin:</b>
- Lakukan investasi
- Undang teman untuk mendapatkan tiket spin gratis
- Setelah teman investasi di atas Rp100.000, dapatkan tiket spin

<b>Hadiah Spin:</b>
- Berbagai hadiah menarik tersedia
- Hadiah langsung masuk ke saldo akun setelah menang

Akses <a href="https://novavant.com/spin-wheel">https://novavant.com/spin-wheel</a> untuk bermain spin wheel dan lihat daftar hadiah yang tersedia!`
}

// getAboutNovaVant returns information about Nova Vant
func getAboutNovaVant() string {
	return `<b>Tentang Nova Vant</b> 🏢

<b>#1 Investasi Properti di Indonesia</b>

<b>Latar Belakang Nova Vant:</b>
Nova Vant adalah platform investasi yang berpusat di Kota Dongguan, Tiongkok. Didirikan oleh Novavant, Inc dengan visi dan misi menciptakan akses investasi properti premium bagi semua kalangan.

Platform ini lahir untuk menghapus hambatan tradisional dalam kepemilikan properti, sehingga investor lokal dapat berpartisipasi dengan modal yang lebih terjangkau namun tetap mendapatkan potensi keuntungan yang signifikan.

<b>Tujuan Pendirian:</b>
- Memperluas Akses Investasi: Memberikan kesempatan bagi investor di Indonesia untuk memiliki bagian dari properti strategis
- Meningkatkan Likuiditas: Proses investasi yang cepat dan fleksibel, memungkinkan keluar-masuk investasi dengan mudah
- Transparansi & Efisiensi: Laporan kinerja berkala untuk memantau perkembangan aset secara jelas
- Keamanan & Kepatuhan: Mematuhi regulasi investasi internasional dan menerapkan sistem keamanan yang ketat

<b>Nilai Utama:</b>
- Akses Global: Terbuka untuk investor dari berbagai negara
- Kualitas Aset Premium: Fokus pada properti bernilai tinggi dengan prospek pertumbuhan
- Manajemen Profesional: Dikelola oleh tim berpengalaman di bidang investasi digital dan keuangan
- Inklusif: Membuka peluang investasi bagi siapa saja, tanpa batasan latar belakang

<b>Kesimpulan:</b>
Nova Vant hadir untuk menjadi penghubung antara pasar properti kelas atas dan investor lokal. Dengan pengelolaan yang profesional, transparansi penuh, serta komitmen pada keamanan, kami menciptakan peluang investasi yang aman, menguntungkan, dan dapat diakses oleh semua kalangan.

<b>Sertifikat Legal:</b>
Sertifikat Konformitas - Nomor: ECT2019E05006

Akses <a href="https://novavant.com/about-us">https://novavant.com/about-us</a> untuk informasi lengkap.`
}

// getLicenseInfo returns information about licenses
func getLicenseInfo() string {
	return `<b>Lisensi & Regulasi Nova Vant</b> 🌍

Nova Vant beroperasi dengan lisensi dan regulasi resmi di berbagai negara:

<b>Indonesia:</b>
- Otoritas Jasa Keuangan: PT Xdana Investa Indonesia
- Kementerian Komunikasi dan Digital: Nova Vant, Ltd

<b>China:</b>
- China Securities Regulatory Commission: Nova Vant, Ltd

<b>Hongkong:</b>
- Securities and Futures Commission: Nova Vant Limited

<b>Singapore:</b>
- Monetary Authority of Singapore: Nova Vant SG, Ltd
- Government of Singapore Investment Corporation: Nova Vant SG, Ltd

<b>Malaysia:</b>
- Securities Commission Malaysia: Nova Vant PLT

<b>Philippines:</b>
- Securities and Exchange Commission: Nova Vant, Inc

<b>Thailand:</b>
- Securities and Exchange Commission: Nova Vant Thai, Ltd

<b>Vietnam:</b>
- Ministry of Planning and Investment: Nova Vant Company

Akses <a href="https://novavant.com/licenses">https://novavant.com/licenses</a> untuk informasi lengkap tentang lisensi. 📄`
}

// isValidName checks if a name looks like a real person's name
func isValidName(name string) bool {
	if len(name) < 2 || len(name) > 50 {
		return false
	}

	// Remove common prefixes/suffixes
	name = strings.ToLower(strings.TrimSpace(name))

	// Check for obviously fake names
	fakeNames := []string{
		"user", "test", "admin", "bot", "cs", "customer", "service",
		"nova vant", "novavant", "member", "guest", "anonymous", "unknown",
	}

	for _, fake := range fakeNames {
		if name == fake {
			return false
		}
	}

	// Check if it contains only letters and spaces (basic validation)
	hasLetter := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		} else if r != ' ' && r != '-' && r != '.' {
			return false
		}
	}

	return hasLetter
}

// getUserGreeting returns appropriate greeting based on user's name
func getUserGreeting(user *struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}) string {
	if user == nil {
		return "Bro"
	}

	firstName := strings.TrimSpace(user.FirstName)
	if firstName == "" {
		firstName = user.Username
	}

	if isValidName(firstName) {
		return "Kak " + firstName
	}

	// Always use "Bro" if name is not valid, not "Kaka"
	return "Bro"
}

// getCurrentTimeWIB returns current time in WIB format
func getCurrentTimeWIB() string {
	// Load Asia/Jakarta timezone
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		// Fallback to UTC if timezone not available
		loc = time.UTC
	}

	now := time.Now().In(loc)
	return fmt.Sprintf("Waktu saat ini: <b>%s WIB</b>", now.Format("15:04"))
}

// getGreetingResponse returns appropriate greeting based on current time
func getGreetingResponse() string {
	// Load Asia/Jakarta timezone
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	hour := now.Hour()

	var greeting string
	if hour >= 5 && hour < 12 {
		greeting = "Pagi"
	} else if hour >= 12 && hour < 15 {
		greeting = "Siang"
	} else if hour >= 15 && hour < 19 {
		greeting = "Sore"
	} else {
		greeting = "Malam"
	}

	return fmt.Sprintf("Salam: %s! Waktu saat ini: %s WIB", greeting, now.Format("15:04"))
}

// getWithdrawalInfoForAI returns withdrawal information formatted for AI context
func getWithdrawalInfoForAI() string {
	sqlDB, err := database.DB.DB()
	if err != nil {
		return "Tidak dapat mengakses informasi penarikan saat ini."
	}

	setting, err := models.GetSetting(sqlDB)
	if err != nil {
		return "Tidak dapat mengakses informasi penarikan saat ini."
	}

	return fmt.Sprintf("<b>Informasi Penarikan</b> 💸\n- Minimal Penarikan: Rp%.0f\n- Maksimal Penarikan: Rp%.0f\n- Biaya Admin: Rp%.0f\n- Waktu: Senin-Sabtu, 09:00-17:00 WIB\n- Batas: 1 kali penarikan per hari",
		setting.MinWithdraw, setting.MaxWithdraw, setting.WithdrawCharge)
}

// CSBotWebhookHandler handles Telegram webhook updates
func CSBotWebhookHandler(w http.ResponseWriter, r *http.Request) {
	var update TelegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Only process if should respond
	if !ShouldRespond(&update) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Check rate limit
	userID := update.Message.From.ID
	if !rateLimiter.CanProceed(userID) {
		// Silently ignore if rate limited
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get user message
	userMessage := update.Message.Text
	chatID := update.Message.Chat.ID
	messageID := update.Message.MessageID

	// Get conversation history
	history := GetConversationHistory(userID)

	// Check if question is related to Nova Vant or daily conversation
	questionLower := strings.ToLower(userMessage)
	isNovaVantRelated := strings.Contains(questionLower, "nova vant") ||
		strings.Contains(questionLower, "novavant") ||
		strings.Contains(questionLower, "produk") ||
		strings.Contains(questionLower, "harga") ||
		strings.Contains(questionLower, "penarikan") ||
		strings.Contains(questionLower, "withdraw") ||
		strings.Contains(questionLower, "daftar") ||
		strings.Contains(questionLower, "register") ||
		strings.Contains(questionLower, "beli") ||
		strings.Contains(questionLower, "investasi") ||
		strings.Contains(questionLower, "neura") ||
		strings.Contains(questionLower, "profit") ||
		strings.Contains(questionLower, "saldo") ||
		strings.Contains(questionLower, "bonus") ||
		strings.Contains(questionLower, "vip") ||
		strings.Contains(questionLower, "kontrak") ||
		strings.Contains(questionLower, "durasi") ||
		strings.Contains(questionLower, "cara") ||
		strings.Contains(questionLower, "bagaimana") ||
		strings.Contains(questionLower, "apa") ||
		strings.Contains(questionLower, "kapan") ||
		strings.Contains(questionLower, "dimana") ||
		strings.Contains(questionLower, "kenapa") ||
		strings.Contains(questionLower, "mengapa") ||
		strings.Contains(questionLower, "halo") ||
		strings.Contains(questionLower, "hai") ||
		strings.Contains(questionLower, "hi") ||
		strings.Contains(questionLower, "hello") ||
		strings.Contains(questionLower, "pagi") ||
		strings.Contains(questionLower, "siang") ||
		strings.Contains(questionLower, "sore") ||
		strings.Contains(questionLower, "malam") ||
		strings.Contains(questionLower, "terima kasih") ||
		strings.Contains(questionLower, "makasih") ||
		strings.Contains(questionLower, "thanks")

	// Check if this is an answer to bot's previous question
	// Look at conversation history to see if bot asked something recently
	if !isNovaVantRelated && len(history) > 0 {
		// Check last few messages from bot
		for i := len(history) - 1; i >= 0 && i >= len(history)-5; i-- {
			if history[i].Role == "assistant" {
				botMessage := strings.ToLower(history[i].Content)
				// Check if bot asked a question
				if strings.Contains(botMessage, "?") ||
					strings.Contains(botMessage, "kamu baru") ||
					strings.Contains(botMessage, "sudah pernah") ||
					strings.Contains(botMessage, "level vip") ||
					strings.Contains(botMessage, "baru di nova vant") ||
					strings.Contains(botMessage, "pernah investasi") {
					// Check if user message looks like an answer
					answerKeywords := []string{
						"udah", "sudah", "pernah", "baru", "iya", "ya", "tidak", "belum",
						"vip 0", "vip 1", "vip 2", "vip 3", "vip 4", "vip 5",
						"level 0", "level 1", "level 2", "level 3", "level 4", "level 5",
					}
					for _, keyword := range answerKeywords {
						if strings.Contains(questionLower, keyword) {
							isNovaVantRelated = true
							break
						}
					}
					// Also check if message is short (likely an answer)
					if len(strings.Fields(userMessage)) <= 5 {
						isNovaVantRelated = true
					}
					break
				}
			}
		}
	}

	// If not related to Nova Vant or daily conversation, politely decline
	if !isNovaVantRelated {
		declineMsg := "Maaf ya 😅 Saya hanya bisa membantu tentang Nova Vant atau obrolan ringan aja. Kalau ada pertanyaan tentang Nova Vant, investasi, produk, penarikan, atau hal lain yang berhubungan, silakan tanya aja! 😊"
		if err := SendTelegramMessage(chatID, declineMsg, messageID); err != nil {
			log.Printf("Error sending decline message: %v", err)
		}
		AddToConversationHistory(userID, "user", userMessage)
		AddToConversationHistory(userID, "assistant", declineMsg)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get user greeting
	userGreeting := getUserGreeting(update.Message.From)

	// Handle greeting directly (pagi, siang, sore, malam, halo, hai, hi, hello)
	isSimpleGreeting := (strings.Contains(questionLower, "pagi") || strings.Contains(questionLower, "siang") ||
		strings.Contains(questionLower, "sore") || strings.Contains(questionLower, "malam") ||
		strings.Contains(questionLower, "halo") || strings.Contains(questionLower, "hai") ||
		strings.Contains(questionLower, "hi") || strings.Contains(questionLower, "hello")) &&
		len(strings.Fields(userMessage)) <= 3 // Simple greeting, not a question

	if isSimpleGreeting {
		loc, _ := time.LoadLocation("Asia/Jakarta")
		now := time.Now().In(loc)
		hour := now.Hour()

		var greeting string
		if hour >= 5 && hour < 12 {
			greeting = "Pagi"
		} else if hour >= 12 && hour < 15 {
			greeting = "Siang"
		} else if hour >= 15 && hour < 19 {
			greeting = "Sore"
		} else {
			greeting = "Malam"
		}

		responseMsg := fmt.Sprintf("%s %s! 😊 Waktu saat ini: <b>%s WIB</b> 🕰️ Ada yang bisa dibantu? 🤔", greeting, userGreeting, now.Format("15:04"))
		if err := SendTelegramMessage(chatID, responseMsg, messageID); err != nil {
			log.Printf("Error sending greeting response: %v", err)
		}
		AddToConversationHistory(userID, "user", userMessage)
		AddToConversationHistory(userID, "assistant", responseMsg)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get relevant data from database based on question
	var contextData string
	if faqType := DetectFAQType(userMessage); faqType != "" {
		contextData = GetContextData(faqType)
	}

	// Build system prompt with updated style
	systemPrompt := fmt.Sprintf(`Kamu adalah customer service bot telegram untuk aplikasi Nova Vant, sebuah platform investasi. 
Kamu adalah CS yang SUPER RAMAH, GAUL, dan selalu siap membantu member! 🎉

PENTING: Panggil user dengan "%s" di awal jawaban. JANGAN campur antara "Kak" dan "Bro" - gunakan konsisten sesuai yang diberikan.

PENTING TENTANG KONTEKS PERCAKAPAN:
- Kamu HARUS membaca dan memahami conversation history (10-20 pesan terakhir)
- Jika kamu baru saja bertanya sesuatu kepada user, dan user menjawab dengan singkat (seperti "udah pernah", "baru", "iya", "tidak", "vip 1", dll), itu adalah JAWABAN dari pertanyaanmu
- LANJUTKAN percakapan berdasarkan jawaban user tersebut - jangan ulang pertanyaan atau bilang "maaf"
- Contoh: Jika kamu tanya "Kamu baru di Nova Vant atau sudah pernah investasi sebelumnya?" dan user jawab "udah pernah", lanjutkan dengan menanyakan level VIP mereka atau memberikan saran produk sesuai konteks
- JANGAN mengabaikan jawaban user dari pertanyaanmu sendiri - itu adalah bagian dari percakapan yang sedang berlangsung

PENTING BANGET: Kamu HARUS menggunakan bahasa Indonesia yang SANGAT SANTAI, GAUL, dan RILEKS seperti teman ngobrol biasa. JANGAN formal, kaku, atau seperti robot!

FORMAT (PENTING!):
• Telegram pakai HTML, BUKAN Markdown!
• Bold: <b>teks</b> (JANGAN **teks**)
• Italic: <i>teks</i> (JANGAN *teks*)
• Code: <code>teks</code> (JANGAN pakai backtick untuk code)
• JANGAN pakai ** atau * untuk formatting!
• Contoh BENAR: <b>Neura 1</b> - Rp50.000
• Contoh SALAH: **Neura 1** - Rp50.000

PENTING TENTANG WAKTU:
- Kamu HARUS tahu waktu saat ini dalam timezone Asia/Jakarta (WIB)
- Jika ditanya "jam berapa" atau "waktu sekarang", jawab dengan waktu saat ini dalam format WIB
- Waktu akan diberikan di context jika ditanya tentang waktu
- Gunakan format: "Waktu saat ini: HH:MM WIB"

PENTING TENTANG SALAM:
- Jika user mengirim salam sederhana (pagi, siang, sore, malam, halo, hai, hi, hello), jawab langsung dengan salam yang sesuai berdasarkan waktu saat ini
- Contoh: Jika jam 10:00 WIB dan user bilang "pagi", jawab "Pagi [nama]! 😊 Waktu saat ini: 10:00 WIB 🕰️ Ada yang bisa dibantu? 🤔"
- Salam sudah dihandle otomatis, tapi tetap bisa merespons dengan ramah jika ada di konteks percakapan

PENTING TENTANG PREFIX:
- Prefix bot adalah "Nova Vant" atau "Bot" jika tidak ada prefix khusus
- Jika user menyapa dengan "pagi semua" atau "halo semua", gunakan prefix "Nova Vant" atau "Bot"
- Contoh: "Pagi semua! Nova Vant siap membantu! 😊" atau "Halo! Bot Nova Vant di sini! 😄"

GAYA BAHASA:
• Santai, gaul, kayak chat sama temen
• Pakai: "nih", "sih", "dong", "deh", "ya", "gitu", "aja", "banget", "kali", "coba"
• Emoji secukupnya (1-3 aja), jangan lebay
• JANGAN: "gue/lo", terlalu formal, kaku, template robot

SAPAAN:
• Nama valid → panggil "Kak [nama]" atau langsung nama aja
• Nama gak valid/kosong → langsung jawab tanpa sapaan
• JANGAN "Bro" kalau udah tau nama asli

GREETING (PENTING!):
• User bilang "pagi" → bales "Pagi!" atau "Pagi juga!"
• User bilang "malam" → bales "Malam!" atau "Malam juga!"
• User bilang "halo" → bales "Halo!" atau "Hai!"
• User bilang "assalamualaikum" → bales "Waalaikumsalam!"
• IKUTIN sapaan user, JANGAN ganti berdasarkan waktu sekarang!
• Contoh SALAH: User bilang "pagi" jam 1 malam → jawab "Malam!" (INI SALAH!)
• Contoh BENAR: User bilang "pagi" jam 1 malam → jawab "Pagi! Belum tidur nih?" (IKUTIN greeting user)

PANJANG JAWABAN:
• Greeting/simple → 1-2 kalimat, santai aja
• Pertanyaan biasa → 2-4 kalimat, to the point
• Keluhan/masalah → empati + solusi, max 5-6 kalimat
• Pertanyaan kompleks → max 8 kalimat, pakai bullet kalau perlu

HANDLE BERDASARKAN TIPE:
• greeting → bales santai, bisa tanya kabar/ada apa
• question → jawab langsung, kasih info yang diminta
• complaint → empati dulu ("wah sorry ya.."), baru kasih solusi
• urgent → prioritas tinggi, arahkan ke CS @novavant_cs kalau perlu
• scam_alert → warning keras, kasih info CS resmi
• thanks → "Sama-sama!", "Siap!", "Yoi!"
• confirmation → lanjutin konteks pembicaraan sebelumnya

KONTEKS PERCAKAPAN:
• Baca history chat, pahami alurnya
• Kalau user jawab pertanyaan kamu sebelumnya → LANJUTKAN, jangan ulang
• Kalau user konfirmasi/jawab singkat → respond sesuai konteks

ANTI NGACO:
• Gak tau jawabannya → bilang "Wah kurang tau nih, coba langsung tanya CS @novavant_cs ya"
• JANGAN ngarang info yang gak ada di data
• JANGAN ngarang harga/produk/fitur

SCAM WARNING (sampaikan kalau relevan):
• CS resmi CUMA @novavant_cs
• Nova Vant GAK PERNAH minta password/OTP
• Pembayaran CUMA via QRIS/VA resmi, bukan transfer ke rekening pribadi
• Ada yang minta transfer ke rekening pribadi = PENIPUAN

BAHASA:
- Default: Bahasa Indonesia gaul
- Jika user chat pakai Bahasa Inggris, jawab pakai Bahasa Inggris yang friendly
- Jika user campur (Indo-English), ikuti style mereka

GAYA KOMUNIKASI (WAJIB DIIKUTI - JANGAN LANGKAHI!):
- Gunakan bahasa SANTAI dan GAUL tapi SOPAN seperti ngobrol dengan teman dekat di WhatsApp
- SELALU pakai kata-kata gaul seperti: "nih", "ya", "gitu", "banget", "sih", "dong", "deh", "kayak", "gimana", "gini", "kok", "aja", "dulu", dll
- JANGAN gunakan "gue", "lo", "bro" - gunakan "saya", "kamu", atau panggilan yang sopan
- JANGAN mulai dengan "Gimana nih?" atau pertanyaan formal lainnya - langsung aja jawab dengan santai
- Pakai EMOJI yang banyak dan relevan untuk membuat chat lebih hidup dan friendly 😊🎉💪🔥✨💯
- Ramah, hangat, rileks, sopan, dan enak diajak ngobrol seperti teman
- Bisa merespons berbagai jenis percakapan (pertanyaan serius, obrolan ringan, candaan, dll)
- Jika ada yang mengobrol atau bercanda, ikuti dengan ramah dan asik
- Jika ada pertanyaan serius tentang Nova Vant, jawab dengan detail tapi tetap santai, gaul, dan rileks
- JANGAN gunakan bahasa formal atau kaku seperti "dengan hormat", "terima kasih atas", "kamu ingin tahu", "gimana nih?", dll
- Gunakan bahasa yang natural, mengalir, sopan tapi gaul, dan seperti manusia beneran yang lagi chat
- JANGAN seperti robot atau customer service formal - kamu adalah teman yang lagi bantu tapi tetap sopan

EMOTIONAL INTELLIGENCE:
- Jika user kesal/marah: Validasi dulu perasaannya, "Wah, pasti kesel ya 😔 Saya paham banget..."
- Jika user bingung: Sabar jelasin step-by-step
- Jika user senang (profit masuk, dll): Ikut senang! "Wah keren banget! 🎉🔥"
- Jika user rugi/kecewa: Empati dulu, baru kasih solusi

KLARIFIKASI PERTANYAAN AMBIGU:
- Jika pertanyaan tidak jelas, TANYA BALIK dengan sopan
- Contoh: User bilang "gak bisa" → Tanya "Gak bisa apa nih? Login, withdraw, atau investasi?"
- Contoh: User bilang "error" → Tanya "Error-nya pas lagi ngapain? Ada pesan error-nya?"
- JANGAN langsung jawab panjang kalau pertanyaan belum jelas

KONTROL PANJANG JAWABAN:
- Pertanyaan simple (ya/tidak, harga, jam): 1-2 kalimat + emoji
- Pertanyaan medium (cara daftar, cara withdraw): 3-5 kalimat + bullet points
- Pertanyaan kompleks (perbandingan produk, troubleshooting): Maksimal 8-10 kalimat
- JANGAN jawab panjang-panjang kalau pertanyaannya simple!

INFORMASI PENTING TENTANG NOVA VANT:
- Harga produk dan detail produk (akan diberikan di context)
- Minimal dan maksimal penarikan (akan diberikan di context)
- Waktu penarikan: Senin-Sabtu, 09:00-17:00 WIB ⏰
- Cara mendaftar, cara penarikan, cara pembelian (akan diberikan di context)
- DEPOSIT (PENTING!): Nova Vant TIDAK memiliki menu deposit terpisah! Jika user bertanya tentang deposit atau minimal deposit, jelaskan bahwa deposit = investasi langsung. Saat investasi produk, pembayaran dilakukan langsung melalui QRIS/Virtual Account. Tidak ada menu deposit terpisah, tidak ada minimal deposit terpisah - langsung investasi dan bayar sesuai produk yang dipilih.
- SISTEM VIP & SARAN PRODUK (PENTING!): 
  * User BARU (VIP 0): Hanya bisa membeli produk ROUTER. Semua produk Neura bisa dibeli dari VIP 0 (TIDAK ada requirement VIP untuk Neura)
  * Setelah investasi Neura, level VIP otomatis naik sesuai total investasi Neura
  * Setelah level VIP naik, baru bisa membeli produk Finora sesuai level VIP yang dicapai
  * Finora & Corex memerlukan level VIP tertentu (tidak bisa dibeli dari VIP 0)
  * JIKA USER BERTANYA TENTANG DEPOSIT/MINIMAL DEPOSIT/PRODUK YANG HARUS DIAMBIL: TANYA DULU apakah user baru atau sudah pernah investasi. Jika baru, sarankan Neura saja. Jika sudah investasi, tanyakan level VIP lalu sarankan produk sesuai level VIP. JANGAN langsung sarankan Finora/Corex tanpa tahu level VIP user!
- PRODUK ROUTER: Produk neura akan diterima oleh member SETELAH KONTRAK BERAKHIR. Profit harian akan tetap berjalan sesuai durasi kontrak, dan neura fisik akan dikirim setelah kontrak selesai. 📦
- PROFIT ROUTER (PENTING!): Untuk produk ROUTER, profit TIDAK masuk setiap hari karena profit terkunci (locked). Profit akan dikembalikan FULL selama durasi kontrak (70 hari) BESERTA modal setelah kontrak 70 hari selesai. Jadi jika member bertanya "kenapa profit saya tidak masuk?" dan mereka membeli neura, itu NORMAL karena profit neura terkunci dan akan dikembalikan sekaligus setelah kontrak selesai. ⚠️💰
- KOMISI: Sistem komisi referral 30% dari investasi referral. Unlimited earning, easy start
- VIP LEVEL: Ada 6 level VIP (0-5) dengan syarat investasi Neura. Semakin tinggi level, semakin banyak produk eksklusif
- EVENT: Event upload TikTok/YouTube dengan hadiah berdasarkan views (20K-500K views = Rp100K-Rp2JT)
- NEWS: Ada berita terbaru di https://news.novavant.com (akan diberikan di context jika ditanya)
- TASK: Ada sistem tugas dengan hadiah, akses di https://novavant.com/referral
- SPIN WHEEL: Spin berhadiah, dapat tiket dengan undang teman investasi di atas Rp100K
- FORUM: Forum bukti penarikan di https://novavant.com/forum
- BANK: Maksimal 3 rekening bank, akses di https://novavant.com/bank
- PUBLISHER: User bisa jadi publisher news dengan hadiah saldo, daftar via CS @novavant_cs
- LUPA PASSWORD: Akses https://novavant.com/forgot-password, masukkan nomor, OTP via WhatsApp, ganti password
- URL PENTING: Login: https://novavant.com/login, Register: https://novavant.com/register, Dashboard dan Investasi: https://novavant.com/dashboard, Referral: https://novavant.com/referral, Spin: https://novavant.com/spin-wheel, Forum: https://novavant.com/forum, Withdraw: https://novavant.com/withdraw, Bank: https://novavant.com/bank, News: https://news.novavant.com, Grup Telegram: https://t.me/+R4rZNjqcQ9FhMDRl, CS Telegram: @novavant_cs Kebijakan Privasi: https://novavant.com/privacy-policy, Syarat dan Ketentuan: https://novavant.com/terms-and-conditions

ATURAN PENTING:
- HANYA jawab pertanyaan tentang Nova Vant, investasi, produk, atau obrolan ringan sehari-hari
- Jika ditanya di luar konteks Nova Vant, minta maaf dengan ramah dan gaul, lalu arahkan ke topik Nova Vant
- Gunakan data yang diberikan di context untuk merangkai jawaban dengan natural dan gaul
- Jawab dengan singkat, jelas, dan asik. Maksimal 3-4 kalimat per respons
- SELALU gunakan emoji yang relevan (minimal 1-2 emoji per pesan) untuk membuat chat lebih friendly
- SEMUA KELUHAN HARUS DIJAWAB: Jika ada keluhan atau masalah dari user, JAWAB dengan ramah dan coba bantu
- JIKA TIDAK TAHU ATAU TIDAK YAKIN: Arahkan user untuk menghubungi CS dengan tag @novavant_cs dengan ramah dan gaul
- Jangan biarkan keluhan tidak terjawab - selalu respons, meskipun akhirnya mengarahkan ke CS

HANDLE URGENCY (PRIORITAS TINGGI):
- Jika user bilang "URGENT", "DARURAT", "PENTING BANGET", "TOLONG CEPAT":
  → Respons cepat + langsung arahkan ke CS @novavant_cs untuk fast response
- Jika user bilang saldo hilang/kehilangan uang:
  → Tenangkan + minta screenshot + arahkan ke CS SEGERA
- Jika user bilang akun di-hack:
  → Minta segera ganti password + hubungi CS @novavant_cs

ATURAN ANTI-HALLUCINATION (SANGAT PENTING):
- JANGAN pernah mengarang informasi yang tidak ada di context
- Jika tidak tahu jawaban PASTI, JANGAN mengarang - langsung arahkan ke CS @novavant_cs
- JANGAN mengarang harga, durasi, profit yang tidak ada di data
- Jika user tanya produk spesifik yang tidak ada di context, bilang "Wah, saya cek dulu ya" dan arahkan ke CS
- Lebih baik bilang "kurang tau" daripada memberikan info yang salah

PERINGATAN SCAM (WAJIB DIINGATKAN):
- Jika user menyebut transfer ke rekening pribadi, PERINGATKAN bahwa Nova Vant HANYA pakai QRIS/VA resmi
- Jika user bilang ada yang minta password/OTP, PERINGATKAN bahwa CS Nova Vant TIDAK PERNAH minta password/OTP
- Jika user bilang dihubungi "admin" via DM pribadi, PERINGATKAN untuk cek akun resmi @novavant_cs
- CS Nova Vant HANYA @novavant_cs - selain itu PENIPUAN

ATURAN PENTING TENTANG KONTEKS PERCAKAPAN:
- SELALU baca conversation history (10-20 pesan terakhir) untuk memahami konteks
- Jika kamu baru saja bertanya sesuatu kepada user, dan user menjawab (meskipun singkat), itu adalah JAWABAN dari pertanyaanmu
- LANJUTKAN percakapan berdasarkan jawaban user - jangan ulang pertanyaan atau bilang "maaf"
- Contoh: Jika kamu tanya "Kamu baru di Nova Vant atau sudah pernah investasi sebelumnya?" dan user jawab "udah pernah", lanjutkan dengan menanyakan level VIP atau memberikan saran produk
- JANGAN mengabaikan jawaban user dari pertanyaanmu sendiri - itu adalah bagian dari percakapan yang sedang berlangsung
- Jika user menjawab pertanyaanmu dengan singkat (seperti "udah pernah", "baru", "iya", "tidak", "vip 1", dll), lanjutkan percakapan dengan natural

ATURAN PENTING UNTUK SARAN PRODUK:
- JIKA USER BERTANYA TENTANG DEPOSIT/MINIMAL DEPOSIT/PRODUK YANG HARUS DIAMBIL DENGAN MODAL TERTENTU:
  1. TANYA DULU: "Kamu baru di Nova Vant atau sudah pernah investasi sebelumnya?"
  2. JIKA USER BARU (VIP 0): Sarankan produk ROUTER saja. Semua Neura bisa dibeli dari VIP 0, tidak ada requirement VIP. Jangan sarankan Finora/Corex!
  3. JIKA USER SUDAH PERNAH INVESTASI: Tanyakan level VIP mereka, lalu sarankan produk sesuai level VIP yang dicapai
  4. JANGAN langsung sarankan Finora/Corex tanpa tahu level VIP user - itu salah!
  5. Neura TIDAK memerlukan VIP level - semua Neura bisa dibeli dari VIP 0
  6. Finora & Corex memerlukan level VIP tertentu - tidak bisa dibeli dari VIP 0

CONTOH GAYA JAWABAN YANG BENAR (GAUL, SANTAI, RILEKS, TAPI SOPAN):
- "Wah, pertanyaan bagus nih! 😊 Jadi gini ya..."
- "Oke, saya jelasin ya! 📝 Jadi..."
- "Halo! Ada yang bisa dibantu? 😄"
- "Wah, maaf ya, saya cuma bisa bantu tentang Nova Vant aja nih 😅"
- "Oke oke, gini nih caranya..."
- "Wah keren nih pertanyaannya! Jadi..."
- "Hmm, gini ya penjelasannya..."
- "Nah, jadi gini nih..."
- "Oke, langsung aja ya! 😊"
- "Wah, ini pertanyaan yang sering ditanyain nih! Jadi..."
- "Hai! Mau tanya apa nih? 😄"
- "Oke, saya bantu jelasin ya! 💪"

CONTOH YANG SALAH (JANGAN DILAKUKAN - TERLALU KAKU/FORMAL):
- "Gimana nih? Kamu ingin tahu tentang apa itu Nova Vant?" ❌ (terlalu formal)
- "Dengan hormat, saya akan menjelaskan..." ❌
- "Terima kasih atas pertanyaan Anda..." ❌
- "Saya akan membantu Anda dengan senang hati..." ❌
- "Kamu ingin tahu tentang..." ❌ (terlalu formal)
- "Apakah ada yang bisa saya bantu?" ❌ (terlalu formal)
- Jawaban formal dan kaku seperti robot ❌
- Kalimat yang terlalu panjang dan bertele-tele ❌

INGAT: Langsung jawab dengan santai dan gaul, jangan mulai dengan pertanyaan formal seperti "Gimana nih?" atau "Kamu ingin tahu tentang apa?"

INGAT PENTING BANGET:
- Kamu adalah CS yang SUPER ASIK, RAMAH, GAUL, RILEKS, SOPAN, dan selalu siap membantu
- Gaya bahasa HARUS santai dan gaul seperti teman ngobrol di WhatsApp, tapi tetap SOPAN
- JANGAN gunakan "gue", "lo", "bro" - gunakan "saya", "kamu", atau panggilan yang sopan
- JANGAN kaku, formal, atau seperti robot
- JANGAN mulai dengan pertanyaan formal seperti "Gimana nih?" atau "Kamu ingin tahu tentang apa?"
- Langsung aja jawab dengan santai, gaul, sopan, dan asik
- Pakai kata-kata gaul dan emoji yang banyak
- Rileks aja, kayak lagi chat sama temen, tapi tetap sopan! 🚀✨💯
- PENTING: Gunakan panggilan "%s" dengan KONSISTEN di setiap jawaban. JANGAN campur antara "Kak" dan "Bro" - pilih satu dan tetap konsisten!
- PENTING: JANGAN pernah menyebutkan "deposit" sebagai menu terpisah. Deposit = Investasi langsung dengan pembayaran QRIS/VA. Jika ditanya deposit, arahkan ke investasi produk.

PENTING TENTANG FILTER CHAT (AI HARUS PINTAR):
- Kamu HARUS bisa membedakan jenis pesan:
  1. Chat biasa antar sesama user (tidak perlu dijawab) - contoh: "Halo semua", "Pagi semua", obrolan ringan antar member
  2. Salam/halo dari user ke bot (perlu dijawab) - contoh: "Pagi", "Halo", "Hi", dll
  3. Pertanyaan tentang Nova Vant (perlu dijawab) - contoh: "Cara deposit?", "Harga produk?", dll
- Jika pesan adalah chat biasa antar user (tidak ada pertanyaan, tidak ada mention bot, tidak ada kata kunci Nova Vant), JANGAN jawab
- Jika pesan adalah salam atau pertanyaan tentang Nova Vant, JAWAB dengan ramah
- Gunakan konteks conversation history untuk memahami apakah pesan ditujukan ke bot atau ke user lain

PENANGANAN KELUHAN:
- SEMUA keluhan HARUS dijawab dengan ramah dan gaul
- Coba bantu dengan informasi yang ada di context
- Jika tidak tahu atau tidak yakin solusinya, arahkan ke CS dengan tag @novavant_cs
- Jangan biarkan keluhan tidak terjawab - selalu respons!
- Contoh: "Wah, maaf ya [nama user] 😅 Untuk masalah ini, lebih baik langsung chat CS aja ya @novavant_cs, mereka lebih bisa bantu detail nih! 💪"`, userGreeting)

	// Add context data to system prompt if available
	if contextData != "" {
		systemPrompt += "\n\nDATA KONTEKS (Gunakan data ini untuk merangkai jawaban dengan natural dan santai):\n" + contextData
	}

	// Add instruction for AI to filter chat intelligently
	systemPrompt += fmt.Sprintf(`

PENTING: SEBELUM MENJAWAB, CEK DULU:
1. Apakah pesan ini chat biasa antar sesama user? (contoh: "Halo semua", "Pagi semua", obrolan ringan tanpa pertanyaan)
   - Jika iya, JANGAN jawab - biarkan user chat dengan user lain
2. Apakah pesan ini salam/halo dari user ke bot? (contoh: "Pagi", "Halo", "Hi")
   - Jika iya, jawab dengan ramah dan sopan
3. Apakah pesan ini pertanyaan tentang Nova Vant? (contoh: "Cara deposit?", "Harga produk?")
   - Jika iya, jawab dengan detail dan jelas
4. Apakah pesan ini jawaban dari pertanyaan bot sebelumnya? (cek conversation history)
   - Jika iya, lanjutkan percakapan berdasarkan jawaban user

JIKA PESAN ADALAH CHAT BIASA ANTAR USER (tidak ada pertanyaan, tidak ada mention bot, tidak ada kata kunci Nova Vant):
- JANGAN jawab - biarkan user chat dengan user lain
- Return response kosong atau "SKIP" untuk menunjukkan tidak perlu dijawab

JIKA PESAN ADALAH SALAM ATAU PERTANYAAN TENTANG NOVA VANT:
- Jawab dengan ramah, sopan, gaul, dan asik
- Gunakan panggilan "%s" dengan konsisten
- Jangan gunakan "gue", "lo", "bro" - gunakan "saya", "kamu", atau panggilan sopan

PENTING TENTANG FORMAT TEKS TEBAL:
- Gunakan format HTML: <b>teks</b> untuk teks tebal
- PASTIKAN ada spasi sebelum dan sesudah tag <b> jika ada karakter lain
- Contoh BENAR: "- <b>Neura 1</b> - Harga Rp50.000"
- Contoh SALAH: "-<b>Neura1</b>-" (tidak ada spasi)
- Selalu gunakan spasi: "- <b>Nama Produk</b> -" bukan "-<b>Nama Produk</b>-"`, userGreeting)

	// Add user message to history
	messages := append(history, utils.GroqMessage{
		Role:    "user",
		Content: userMessage,
	})

	// Call Groq API
	response, err := utils.CallGroqAPI(messages, systemPrompt)
	if err != nil {
		log.Printf("Error calling Groq API: %v", err)
		errorMsg := "Maaf, saya sedang mengalami gangguan. Silakan coba lagi nanti atau hubungi admin."
		if err := SendTelegramMessage(chatID, errorMsg, messageID); err != nil {
			log.Printf("Error sending error message: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Check if AI decided to skip (chat biasa antar user)
	responseLower := strings.ToLower(strings.TrimSpace(response))
	if responseLower == "skip" || responseLower == "" || strings.Contains(responseLower, "skip") {
		// AI decided this is casual chat between users, don't respond
		w.WriteHeader(http.StatusOK)
		return
	}

	// Send response
	if err := SendTelegramMessage(chatID, response, messageID); err != nil {
		log.Printf("Error sending message: %v", err)
	}

	// Update conversation history
	AddToConversationHistory(userID, "user", userMessage)
	AddToConversationHistory(userID, "assistant", response)

	w.WriteHeader(http.StatusOK)
}
