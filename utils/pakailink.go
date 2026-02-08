package utils

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	pakailinkChannelID = "95222" // Browser
)

func getPakailinkConfig() (baseURL, clientKey, clientSecret, partnerID, privateKeyPath, callbackURL, merchantID, storeID, terminalID string, err error) {
	baseURL = os.Getenv("PAKAILINK_BASE_URL")
	clientKey = os.Getenv("PAKAILINK_CLIENT_KEY")
	clientSecret = os.Getenv("PAKAILINK_CLIENT_SECRET")
	partnerID = os.Getenv("PAKAILINK_PARTNER_ID")
	privateKeyPath = os.Getenv("PAKAILINK_PRIVATE_KEY_PATH")
	callbackURL = os.Getenv("PAKAILINK_PAYMENT_CALLBACK_URL")
	merchantID = os.Getenv("PAKAILINK_MERCHANT_ID")
	storeID = os.Getenv("PAKAILINK_STORE_ID")
	terminalID = os.Getenv("PAKAILINK_TERMINAL_ID")

	if baseURL == "" {
		baseURL = "https://api.pakailink.com"
	}
	if clientKey == "" || clientSecret == "" || partnerID == "" || privateKeyPath == "" || callbackURL == "" {
		return "", "", "", "", "", "", "", "", "", fmt.Errorf("PAKAILINK config wajib")
	}
	return baseURL, clientKey, clientSecret, partnerID, privateKeyPath, callbackURL, merchantID, storeID, terminalID, nil
}

func getPakailinkQRISConfig() (merchantID, storeID, terminalID string, err error) {
	merchantID = os.Getenv("PAKAILINK_MERCHANT_ID")
	storeID = os.Getenv("PAKAILINK_STORE_ID")
	terminalID = os.Getenv("PAKAILINK_TERMINAL_ID")
	if merchantID == "" || storeID == "" || terminalID == "" {
		return "", "", "", fmt.Errorf("PAKAILINK_MERCHANT_ID, PAKAILINK_STORE_ID, PAKAILINK_TERMINAL_ID wajib untuk QRIS")
	}
	return merchantID, storeID, terminalID, nil
}

var VABankCodes = map[string]string{
	"BCA": "014", "BNI": "009", "BRI": "002", "BSI": "451", "CIMB": "022",
	"DANAMON": "011", "MANDIRI": "008", "BMI": "147", "BNC": "490",
	"OCBC": "028", "PERMATA": "013", "SINARMAS": "153", "PANIN": "019", "MAYBANK": "016",
}

func GetVABankCode(channel string) string {
	s := strings.ToUpper(strings.TrimSpace(channel))
	if code, ok := VABankCodes[s]; ok {
		return code
	}
	return s
}

func pakailinkTimestamp() string {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	return time.Now().In(loc).Format("2006-01-02T15:04:05-07:00")
}

func createAsymmetricSignature(stringToSign, privateKeyPath string) (string, error) {
	cleanPath := filepath.Clean(privateKeyPath)
	keyData, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("baca private key %s: %w", cleanPath, err)
	}
	block, _ := pem.Decode(keyData)
	if block == nil {
		return "", fmt.Errorf("invalid PEM")
	}
	privKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		privKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	if err != nil {
		return "", err
	}
	rsaKey, ok := privKey.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("bukan RSA private key")
	}
	h := sha256.Sum256([]byte(stringToSign))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// createSymmetricSignature for VA/QRIS: HMAC-SHA512(bodyHash, clientSecret) -> Base64
// StringToSign: METHOD:RELATIVE_PATH:ACCESS_TOKEN:BODY_HASH:TIMESTAMP
func createSymmetricSignature(method, path, accessToken string, body []byte, timestamp, clientSecret string) string {
	bodyHash := sha256.Sum256(body)
	bodyHashHex := strings.ToLower(hex.EncodeToString(bodyHash[:]))
	stringToSign := method + ":" + path + ":" + accessToken + ":" + bodyHashHex + ":" + timestamp
	mac := hmac.New(sha512.New, []byte(clientSecret))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func minifyJSON(body []byte) []byte {
	var m map[string]interface{}
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	out, _ := json.Marshal(m)
	return out
}

func generateExternalID() string {
	return strconv.FormatInt(time.Now().UnixNano()%10000000000, 10)
}

// PakailinkAccessTokenResponse from /snap/v1.0/access-token/b2b
type PakailinkAccessTokenResponse struct {
	ResponseCode    string `json:"responseCode"`
	ResponseMessage string `json:"responseMessage"`
	AccessToken     string `json:"accessToken"`
	TokenType       string `json:"tokenType"`
	ExpiresIn       string `json:"expiresIn"`
}

// GetPakailinkAccessToken obtains B2B token using asymmetric signature
func GetPakailinkAccessToken(ctx context.Context, client *http.Client) (string, error) {
	_, clientKey, _, _, privateKeyPath, _, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return "", err
	}
	baseURL := os.Getenv("PAKAILINK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.pakailink.com"
	}
	path := "/snap/v1.0/access-token/b2b"
	url := strings.TrimRight(baseURL, "/") + path

	timestamp := pakailinkTimestamp()
	stringToSign := clientKey + "|" + timestamp
	sig, err := createAsymmetricSignature(stringToSign, privateKeyPath)
	if err != nil {
		return "", err
	}

	body := []byte(`{"grantType":"client_credentials"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-CLIENT-KEY", clientKey)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var tok PakailinkAccessTokenResponse
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return "", fmt.Errorf("parse token: %w (body: %s)", err, string(respBody))
	}
	if tok.ResponseCode != "2007300" {
		return "", fmt.Errorf("API %s: %s", tok.ResponseCode, tok.ResponseMessage)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("token kosong")
	}
	return tok.AccessToken, nil
}

// PakailinkCreateVAResponse from create-va
type PakailinkCreateVAResponse struct {
	ResponseCode       string `json:"responseCode"`
	ResponseMessage    string `json:"responseMessage"`
	VirtualAccountData struct {
		VirtualAccountNo   string `json:"virtualAccountNo"`
		PartnerReferenceNo string `json:"partnerReferenceNo"`
		ExpiredDate        string `json:"expiredDate"`
		TotalAmount        struct {
			Value    string `json:"value"`
			Currency string `json:"currency"`
		} `json:"totalAmount"`
		AdditionalInfo struct {
			CallbackURL string `json:"callbackUrl"`
			BankCode    string `json:"bankCode"`
			ReferenceNo string `json:"referenceNo"`
		} `json:"additionalInfo"`
	} `json:"virtualAccountData"`
}

// CreatePakailinkVA creates VA (1 day expiry)
func CreatePakailinkVA(ctx context.Context, client *http.Client, accessToken, partnerRefNo, customerNo, vaName string, amount float64, bankCode string) (*PakailinkCreateVAResponse, error) {
	baseURL, _, clientSecret, partnerID, _, callbackURL, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/transfer-va/create-va"
	url := strings.TrimRight(baseURL, "/") + path

	expired := time.Now().Add(24 * time.Hour) // 1 day
	loc, _ := time.LoadLocation("Asia/Jakarta")
	expiredStr := expired.In(loc).Format("2006-01-02T15:04:05-07:00")

	bodyObj := map[string]interface{}{
		"partnerReferenceNo": partnerRefNo,
		"customerNo":         customerNo,
		"virtualAccountName": vaName,
		"expiredDate":        expiredStr,
		"totalAmount":        map[string]string{"value": fmt.Sprintf("%.2f", amount), "currency": "IDR"},
		"additionalInfo": map[string]string{
			"callbackUrl": callbackURL,
			"bankCode":    bankCode,
		},
	}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkCreateVAResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2002700" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// PakailinkCreateQRResponse from qr-mpm-generate
type PakailinkCreateQRResponse struct {
	ResponseCode       string `json:"responseCode"`
	ResponseMessage    string `json:"responseMessage"`
	QRContent          string `json:"qrContent"`
	PartnerReferenceNo string `json:"partnerReferenceNo"`
	ValidityPeriod     string `json:"validityPeriod"`
	Amount             struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`
}

// CreatePakailinkQRIS creates QRIS MPM (1 day expiry)
func CreatePakailinkQRIS(ctx context.Context, client *http.Client, accessToken, partnerRefNo string, amount float64) (*PakailinkCreateQRResponse, error) {
	baseURL, _, clientSecret, partnerID, _, callbackURL, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	merchantID, storeID, terminalID, err := getPakailinkQRISConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/qr/qr-mpm-generate"
	url := strings.TrimRight(baseURL, "/") + path

	expired := time.Now().Add(24 * time.Hour)
	loc, _ := time.LoadLocation("Asia/Jakarta")
	expiredStr := expired.In(loc).Format("2006-01-02T15:04:05-07:00")

	bodyObj := map[string]interface{}{
		"merchantId":         merchantID,
		"storeId":            storeID,
		"terminalId":         terminalID,
		"partnerReferenceNo": partnerRefNo,
		"amount":             map[string]string{"value": fmt.Sprintf("%.2f", amount), "currency": "IDR"},
		"validityPeriod":     expiredStr,
		"additionalInfo": map[string]string{
			"callbackUrl": callbackURL,
			"type":        "dinamis",
		},
	}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkCreateQRResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2004700" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// PakailinkVAStatusResponse from inquiry VA
type PakailinkVAStatusResponse struct {
	ResponseCode               string `json:"responseCode"`
	ResponseMessage            string `json:"responseMessage"`
	OriginalPartnerReferenceNo string `json:"originalPartnerReferenceNo"`
	LatestTransactionStatus    string `json:"latestTransactionStatus"`
	TransactionStatusDesc      string `json:"transactionStatusDesc"`
	Amount                     struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`
}

// InquiryPakailinkVAStatus checks VA payment status
func InquiryPakailinkVAStatus(ctx context.Context, client *http.Client, accessToken, partnerRefNo string) (*PakailinkVAStatusResponse, error) {
	baseURL, _, clientSecret, partnerID, _, _, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/transfer-va/create-va-status"
	url := strings.TrimRight(baseURL, "/") + path

	bodyObj := map[string]string{"originalPartnerReferenceNo": partnerRefNo}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkVAStatusResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2003300" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// PakailinkQRStatusResponse from inquiry QR
type PakailinkQRStatusResponse struct {
	ResponseCode               string `json:"responseCode"`
	ResponseMessage            string `json:"responseMessage"`
	OriginalPartnerReferenceNo string `json:"originalPartnerReferenceNo"`
	LatestTransactionStatus    string `json:"latestTransactionStatus"`
	TransactionStatusDesc      string `json:"transactionStatusDesc"`
	Amount                     struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`
}

// InquiryPakailinkQRStatus checks QRIS payment status
func InquiryPakailinkQRStatus(ctx context.Context, client *http.Client, accessToken, partnerRefNo string) (*PakailinkQRStatusResponse, error) {
	baseURL, _, clientSecret, partnerID, _, _, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/qr/qr-mpm-status"
	url := strings.TrimRight(baseURL, "/") + path

	bodyObj := map[string]string{"originalPartnerReferenceNo": partnerRefNo}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkQRStatusResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2005300" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// IsPakailinkSuccessStatus returns true if latestTransactionStatus = "00"
func IsPakailinkSuccessStatus(status string) bool {
	return strings.TrimSpace(status) == "00"
}

// GetPakailinkPayoutCallbackURL returns payout callback URL from env
func GetPakailinkPayoutCallbackURL() string {
	u := os.Getenv("PAKAILINK_PAYOUT_CALLBACK_URL")
	if u == "" {
		return os.Getenv("PAKAILINK_PAYMENT_CALLBACK_URL") // fallback
	}
	return u
}

// PakailinkBankInquiryResponse from bank-account-inquiry
type PakailinkBankInquiryResponse struct {
	ResponseCode           string `json:"responseCode"`
	ResponseMessage        string `json:"responseMessage"`
	SessionID              string `json:"sessionId"`
	PartnerReferenceNo     string `json:"partnerReferenceNo"`
	BeneficiaryAccountNo   string `json:"beneficiaryAccountNumber"`
	BeneficiaryAccountName string `json:"beneficiaryAccountName"`
	BeneficiaryBankName    string `json:"beneficiaryBankName"`
}

// PakailinkBankInquiry validates bank account
func PakailinkBankInquiry(ctx context.Context, client *http.Client, accessToken, partnerRefNo, accountNo, bankCode string) (*PakailinkBankInquiryResponse, error) {
	baseURL, _, clientSecret, partnerID, _, _, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/emoney/bank-account-inquiry"
	url := strings.TrimRight(baseURL, "/") + path

	bodyObj := map[string]interface{}{
		"partnerReferenceNo":       partnerRefNo,
		"beneficiaryAccountNumber": accountNo,
		"additionalInfo":           map[string]string{"beneficiaryBankCode": bankCode},
	}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkBankInquiryResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2004200" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// PakailinkBankTransferResponse from transfer-bank
type PakailinkBankTransferResponse struct {
	ResponseCode       string `json:"responseCode"`
	ResponseMessage    string `json:"responseMessage"`
	ReferenceNo        string `json:"referenceNo"`
	PartnerReferenceNo string `json:"partnerReferenceNo"`
	AdditionalInfo     struct {
		TransactionStatus string `json:"transactionStatus"`
	} `json:"additionalInfo"`
}

// PakailinkBankTransfer executes bank transfer
func PakailinkBankTransfer(ctx context.Context, client *http.Client, accessToken, partnerRefNo, accountNo, bankCode, sessionID string, amount int64, callbackURL string) (*PakailinkBankTransferResponse, error) {
	baseURL, _, clientSecret, partnerID, _, _, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/emoney/transfer-bank"
	url := strings.TrimRight(baseURL, "/") + path

	addInfo := map[string]interface{}{"remark": ""}
	if callbackURL != "" {
		addInfo["callbackUrl"] = callbackURL
	}
	bodyObj := map[string]interface{}{
		"partnerReferenceNo":       partnerRefNo,
		"beneficiaryAccountNumber": accountNo,
		"beneficiaryBankCode":      bankCode,
		"amount":                   map[string]string{"value": fmt.Sprintf("%.2f", float64(amount)), "currency": "IDR"},
		"additionalInfo":           addInfo,
	}
	if sessionID != "" {
		bodyObj["sessionId"] = sessionID
	}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkBankTransferResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2004300" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// PakailinkEwalletInquiryResponse from account-inquiry (ewallet)
type PakailinkEwalletInquiryResponse struct {
	ResponseCode       string `json:"responseCode"`
	ResponseMessage    string `json:"responseMessage"`
	SessionID          string `json:"sessionId"`
	PartnerReferenceNo string `json:"partnerReferenceNo"`
	CustomerNumber     string `json:"customerNumber"`
	CustomerName       string `json:"customerName"`
}

// PakailinkEwalletInquiry validates ewallet account
func PakailinkEwalletInquiry(ctx context.Context, client *http.Client, accessToken, partnerRefNo, customerNo, productCode string) (*PakailinkEwalletInquiryResponse, error) {
	baseURL, _, clientSecret, partnerID, _, _, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/emoney/account-inquiry"
	url := strings.TrimRight(baseURL, "/") + path

	bodyObj := map[string]interface{}{
		"partnerReferenceNo": partnerRefNo,
		"customerNumber":     customerNo,
		"additionalInfo":     map[string]string{"productCode": productCode},
	}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkEwalletInquiryResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2003700" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// PakailinkEwalletTopupResponse from topup
type PakailinkEwalletTopupResponse struct {
	ResponseCode       string `json:"responseCode"`
	ResponseMessage    string `json:"responseMessage"`
	ReferenceNo        string `json:"referenceNo"`
	PartnerReferenceNo string `json:"partnerReferenceNo"`
	AdditionalInfo     struct {
		TransactionStatus string `json:"transactionStatus"`
	} `json:"additionalInfo"`
}

// PakailinkEwalletTopup executes ewallet topup (disbursement)
func PakailinkEwalletTopup(ctx context.Context, client *http.Client, accessToken, partnerRefNo, customerNo, productCode, sessionID string, amount int64, callbackURL string) (*PakailinkEwalletTopupResponse, error) {
	baseURL, _, clientSecret, partnerID, _, _, _, _, _, err := getPakailinkConfig()
	if err != nil {
		return nil, err
	}
	path := "/snap/v1.0/emoney/topup"
	url := strings.TrimRight(baseURL, "/") + path

	addInfo := map[string]interface{}{}
	if callbackURL != "" {
		addInfo["callbackUrl"] = callbackURL
	}
	bodyObj := map[string]interface{}{
		"partnerReferenceNo": partnerRefNo,
		"customerNumber":     customerNo,
		"productCode":        productCode,
		"amount":             map[string]string{"value": fmt.Sprintf("%.2f", float64(amount)), "currency": "IDR"},
		"additionalInfo":     addInfo,
	}
	if sessionID != "" {
		bodyObj["sessionId"] = sessionID
	}
	body, _ := json.Marshal(bodyObj)
	bodyMinified := minifyJSON(body)

	timestamp := pakailinkTimestamp()
	externalID := generateExternalID()
	sig := createSymmetricSignature("POST", path, accessToken, bodyMinified, timestamp, clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyMinified))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-TIMESTAMP", timestamp)
	req.Header.Set("X-PARTNER-ID", partnerID)
	req.Header.Set("X-EXTERNAL-ID", externalID)
	req.Header.Set("CHANNEL-ID", pakailinkChannelID)
	req.Header.Set("X-SIGNATURE", sig)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result PakailinkEwalletTopupResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.ResponseCode != "2003800" {
		return nil, fmt.Errorf("%s: %s", result.ResponseCode, result.ResponseMessage)
	}
	return &result, nil
}

// GetPakailinkPayoutCode returns bank code (014) or ewallet productCode (DANA) for Pakailink
// Deprecated: use bank.Code directly - code column now stores gateway code
func GetPakailinkPayoutCode(bankCode, pakailinkCode string, bankType string) string {
	if pakailinkCode != "" {
		return pakailinkCode
	}
	if bankType == "ewallet" {
		return bankCode
	}
	return GetVABankCode(bankCode)
}
