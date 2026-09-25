package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime" // 🎯 引入 Wails runtime 事件通訊
	"golang.org/x/text/encoding/traditionalchinese" // 用於處理 Big5 編碼轉換（如 MoneyDJ 等台灣舊式網頁）
	"golang.org/x/text/transform"                   // 用於字串編碼轉換的資料流處理
)

// =========================================================
// Wails 應用程式核心結構
// =========================================================

// App 定義了 Wails 應用程式的後端實體，包含應用程式的上下文 (Context)
type App struct {
	ctx context.Context // 儲存 Wails 執行環境的 Context，用於發送事件或控制視窗
}

// NewApp 建立並回傳一個新的 App 實體，作為 Wails 啟動時的入口
func NewApp() *App { return &App{} }

// startup 在 Wails 應用程式啟動時被呼叫，進行初始化設定
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// 🎯 啟動後稍微延遲 500 毫秒，確保前端 React 已經掛載並開始監聽事件，再執行資料檢查
	// 透過 Goroutine 讓檢查在背景執行，不阻塞 Wails 主視窗的渲染
	go func() {
		time.Sleep(500 * time.Millisecond)
		a.CheckAndPrepareData()
	}()
}

// =========================================================
// 🎯 啟動資料檢查與強制下載機制 (解決 N/A 問題)
// =========================================================

// CheckAndPrepareData 檢查必要數據，若缺失則通知前端並執行下載
func (a *App) CheckAndPrepareData() {
	go func() {
		for i := 0; i < 5; i++ {
			runtime.EventsEmit(a.ctx, "data_ready", true)
			time.Sleep(1 * time.Second)
		}
	}()

	fileName := "tdcc_cache.csv"
	info, err := os.Stat(fileName)

	if err == nil && info.Size() < 10000000 {
		fmt.Printf("[System] 偵測到損壞或未完成的集保快取檔案 (%.2f MB)，執行自動刪除清理...\n", float64(info.Size())/1024/1024)
		os.Remove(fileName)
		os.Remove(fileName + ".tmp")
		err = os.ErrNotExist
	}

	isValid := err == nil && info.Size() >= 10000000

	if !isValid {
		fmt.Println("[System] 偵測到本機無完整的集保籌碼檔案，啟動背景靜默下載 (不卡畫面)...")
		go a.ForceDownloadTDCC()
	} else {
		now := time.Now()
		modTime := info.ModTime()
		isToday := now.Year() == modTime.Year() && now.YearDay() == modTime.YearDay()

		if !isToday {
			fmt.Println("[System] 偵測到集保檔案非當日最新，啟動背景無感下載更新 (不影響畫面操作)...")
			go a.ForceDownloadTDCC()
		} else {
			fmt.Println("[System] 核心資料已備妥且為當日最新，無需更新。")
		}
	}
}

// ForceDownloadTDCC 專門負責強制下載並發送進度事件
func (a *App) ForceDownloadTDCC() {
	if !atomic.CompareAndSwapInt32(&tdccDownloadFlag, 0, 1) { 
		return
	}
	defer atomic.StoreInt32(&tdccDownloadFlag, 0)

	downloadTDCCRobust("tdcc_cache.csv")
}

// =========================================================
// 🎯 核心強化：全域防封鎖與重試 HTTP 請求函數
// =========================================================

func httpGetWithRetry(url string) (*http.Response, error) {
	customTransport := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		ForceAttemptHTTP2: false,
		DisableKeepAlives: true,
	}

	client := &http.Client{
		Timeout:   4 * time.Second,
		Transport: customTransport,
	}

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,en-US;q=0.8,en;q=0.7")
		req.Header.Set("Cache-Control", "max-age=0")
		req.Header.Set("Connection", "close")
		
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			return resp, nil 
		}
		
		if resp != nil { resp.Body.Close() }
		time.Sleep(300 * time.Millisecond)
	}
	return nil, fmt.Errorf("HTTP 請求失敗，已達最大重試次數")
}

func parseFinancialNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	
	isNegative := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") ||
		strings.Contains(s, "△") || strings.Contains(s, "▼") || strings.Contains(s, "▽") ||
		strings.Contains(s, "虧") || strings.Contains(s, "負")
	
	var clean strings.Builder
	for _, ch := range s {
		if (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' {
			clean.WriteRune(ch)
		} else if ch == '－' || ch == '−' || ch == '—' || ch == '–' {
			clean.WriteRune('-')
		}
	}
	
	val := clean.String()
	if val == "" || val == "-" || val == "--" || val == "." { 
		return 0, fmt.Errorf("empty or invalid value") 
	}

	f, err := strconv.ParseFloat(val, 64)
	if err != nil { 
		return 0, err 
	}
	
	if isNegative && f > 0 { f = -f }

	return f, nil
}

func cleanText(s string) string {
	var res strings.Builder
	inTag := false
	for _, ch := range s {
		if ch == '<' { 
			inTag = true
		} else if ch == '>' { 
			inTag = false
		} else if !inTag { 
			res.WriteRune(ch)
		}
	}
	
	out := res.String()
	out = strings.ReplaceAll(out, "&nbsp;", "")
	out = strings.ReplaceAll(out, "\u00A0", "")
	out = strings.ReplaceAll(out, " ", "")
	out = strings.ReplaceAll(out, "\t", "")
	out = strings.ReplaceAll(out, "\n", "")
	out = strings.ReplaceAll(out, "\r", "")
	return out
}

func extractCells(row string) []string {
	var cells []string
	lowerRow := strings.ToLower(row)
	
	for {
		idxTD := strings.Index(lowerRow, "<td")
		idxTH := strings.Index(lowerRow, "<th")
		
		if idxTD == -1 && idxTH == -1 {
			break
		}
		
		startIdx := -1
		tagType := ""
		
		if idxTD != -1 && idxTH != -1 {
			if idxTD < idxTH {
				startIdx = idxTD
				tagType = "td"
			} else {
				startIdx = idxTH
				tagType = "th"
			}
		} else if idxTD != -1 {
			startIdx = idxTD
			tagType = "td"
		} else {
			startIdx = idxTH
			tagType = "th"
		}
		
		lowerRow = lowerRow[startIdx:]
		row = row[startIdx:]
		
		closeBr := strings.Index(lowerRow, ">")
		if closeBr == -1 { break }
		
		lowerRow = lowerRow[closeBr+1:]
		row = row[closeBr+1:]
		
		endTag := "</" + tagType
		endIdx := strings.Index(lowerRow, endTag)
		
		if endIdx == -1 {
			altTag := "</td>"
			if tagType == "td" { altTag = "</th>" }
			endIdx = strings.Index(lowerRow, altTag)
		}
		
		if endIdx != -1 {
			content := row[:endIdx]
			cells = append(cells, cleanText(content))
			lowerRow = lowerRow[endIdx:]
			row = row[endIdx:]
		} else {
			break
		}
	}
	return cells
}

func getYahooQuote(stockID string) (price float64, pe float64, pb float64, eps float64) {
	yahooAuthMu.Lock()
	crumb := yahooCrumb
	cookie := yahooCookie
	yahooAuthMu.Unlock()

	if crumb == "" {
		refreshYahooAuth()
		yahooAuthMu.Lock()
		crumb = yahooCrumb
		cookie = yahooCookie
		yahooAuthMu.Unlock()
	}

	client := &http.Client{Timeout: 4 * time.Second}
	urlStr := fmt.Sprintf("https://query1.finance.yahoo.com/v7/finance/quote?symbols=%s.TW,%s.TWO", stockID, stockID)
	if crumb != "" { urlStr += "&crumb=" + crumb }
	
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if cookie != "" { req.Header.Set("Cookie", cookie) }
	
	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == 200 {
		var res map[string]interface{}
		if json.NewDecoder(resp.Body).Decode(&res) == nil {
			if qr, ok := res["quoteResponse"].(map[string]interface{}); ok {
				if results, ok := qr["result"].([]interface{}); ok && len(results) > 0 {
					if data, ok := results[0].(map[string]interface{}); ok {
						if p, ok := data["regularMarketPrice"].(float64); ok && p > 0 {
							price = p
						}
						if peVal, ok := data["trailingPE"].(float64); ok && peVal > 0 {
							pe = peVal
						}
						if pbVal, ok := data["priceToBook"].(float64); ok && pbVal > 0 {
							pb = pbVal
						}
						if epsVal, ok := data["epsTrailingTwelveMonths"].(float64); ok {
							eps = epsVal
						}
						resp.Body.Close()
						return price, pe, pb, eps
					}
				}
			}
		}
		resp.Body.Close()
	} else if resp != nil {
		resp.Body.Close()
	}
	return 0, 0, 0, 0
}

func resolveStock(input string) (resolvedID string, price float64, name string, err error) {
	input = strings.TrimSpace(input)
	type Company struct { ID, Name string }
	var companies []Company

	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(4)

	go func() {
		defer wg.Done()
		if resp, err := httpGetWithRetry("https://openapi.twse.com.tw/v1/opendata/t187ap03_L"); err == nil {
			var data []map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&data) == nil {
				mu.Lock()
				for _, c := range data {
					companies = append(companies, Company{ID: fmt.Sprintf("%v", c["公司代號"]), Name: fmt.Sprintf("%v", c["公司名稱"])})
				}
				mu.Unlock()
			}
			resp.Body.Close()
		}
	}()

	go func() {
		defer wg.Done()
		if resp, err := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/mopsfin_t187ap03_O"); err == nil {
			var data []map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&data) == nil {
				mu.Lock()
				for _, c := range data {
					companies = append(companies, Company{ID: fmt.Sprintf("%v", c["公司代號"]), Name: fmt.Sprintf("%v", c["公司名稱"])})
				}
				mu.Unlock()
			}
			resp.Body.Close()
		}
	}()

	go func() {
		defer wg.Done()
		if resp, err := httpGetWithRetry("https://www.twse.com.tw/exchangeReport/STOCK_DAY_ALL?response=json"); err == nil {
			var res map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&res) == nil {
				if dataList, ok := res["data"].([]interface{}); ok {
					mu.Lock()
					for _, item := range dataList {
						row := item.([]interface{})
						companies = append(companies, Company{ID: fmt.Sprintf("%v", row[0]), Name: fmt.Sprintf("%v", row[1])})
					}
					mu.Unlock()
				}
			}
			resp.Body.Close()
		}
	}()

	go func() {
		defer wg.Done()
		if resp, err := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/tpex_mainboard_quotes"); err == nil {
			var data []map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&data) == nil {
				mu.Lock()
				for _, c := range data {
					companies = append(companies, Company{ID: fmt.Sprintf("%v", c["SecuritiesCompanyCode"]), Name: fmt.Sprintf("%v", c["CompanyName"])})
				}
				mu.Unlock()
			}
			resp.Body.Close()
		}
	}()

	wg.Wait()
	
	var targetID, targetName string
	for _, c := range companies {
		cleanID := strings.TrimSpace(c.ID)
		cleanName := strings.TrimSpace(c.Name)
		if cleanID == input || cleanName == input {
			targetID, targetName = cleanID, cleanName
			break
		}
	}

	if targetID == "" {
		for _, c := range companies {
			if strings.Contains(strings.TrimSpace(c.Name), input) {
				targetID, targetName = strings.TrimSpace(c.ID), strings.TrimSpace(c.Name)
				break
			}
		}
	}
	
	if targetID == "" {
		isNum := true
		for _, ch := range input {
			if ch < '0' || ch > '9' { isNum = false; break }
		}
		if isNum && len(input) > 0 {
			targetID = input
			targetName = "未知名稱"
		} else {
			return "", 0, "", fmt.Errorf("查無此股票，請確認名稱或代號")
		}
	}
	
	price, _, _, _ = getYahooQuote(targetID)
	
	if price == 0 {
		if respTWSE, errTWSE := httpGetWithRetry("https://www.twse.com.tw/exchangeReport/STOCK_DAY_ALL?response=json"); errTWSE == nil {
			var result map[string]interface{}
			if json.NewDecoder(respTWSE.Body).Decode(&result) == nil {
				if dataList, ok := result["data"].([]interface{}); ok {
					for _, item := range dataList {
						row := item.([]interface{})
						if strings.TrimSpace(fmt.Sprintf("%v", row[0])) == targetID {
							price, _ = strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", row[7]), ",", ""), 64)
							break
						}
					}
				}
			}
			respTWSE.Body.Close()
		}
	}
	if price == 0 {
		if respOTC, errOTC := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/tpex_mainboard_quotes"); errOTC == nil {
			var tpexData []map[string]interface{}
			bodyOtc, _ := io.ReadAll(respOTC.Body)
			if json.Unmarshal(bodyOtc, &tpexData) == nil {
				for _, item := range tpexData {
					if strings.TrimSpace(fmt.Sprintf("%v", item["SecuritiesCompanyCode"])) == targetID {
						price, _ = strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", item["Close"]), ",", ""), 64)
						break
					}
				}
			}
			respOTC.Body.Close()
		}
	}
	
	return targetID, price, targetName, nil
}

// =========================================================
// 分頁一：大盤與潛力股
// =========================================================

// 🎯 Top10Stock 結構
type Top10Stock struct {
	Indicator  string `json:"indicator"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Price      string `json:"price"`
	Change     string `json:"change"`
	Feature    string `json:"feature"`
	Date       string `json:"date"`       // 雲端資料真實日期
	Days       int    `json:"days"`
	Summary    string `json:"summary"`
	Rule       string `json:"rule"`
	MarketDate string `json:"marketDate"` // 今日交易日 (如: 2026-09-25)
	IsUpToDate bool   `json:"isUpToDate"` // 是否已同步為今日最新
}

type sortableStock struct {
	Top10Stock
	Value float64 
	Days  int
}

// 🎯 取得各技術指標策略的標準買賣點說明（支援 Volume Ratio 等多種別名）
func getIndicatorRule(ind string) string {
	switch ind {
	case "RSI":
		return "RSI<30 買進，RSI>70 賣出"
	case "KD":
		return "KD<20 買進，KD>80 賣出"
	case "MACD":
		return "MACD柱狀圖負轉正(黃金交叉)買進，正轉負(死亡交叉)賣出"
	case "SMA":
		return "股價突破20MA(月線)買進，跌破20MA賣出"
	case "BollingerBands", "Bollinger Bands", "Bollinger":
		return "股價跌破布林下軌買進，突破上軌賣出"
	case "Momentum":
		return "動能指標由負轉正買進，由正轉負賣出"
	case "VolumeRatio", "Volume Ratio", "ChipRatio", "成交量均量比", "成交量均量比策略（Volume Ratio）":
		return "10日均量比值>1.5買進 ； 跌破10MA或均量比>2.0且實體長黑K平倉"
	case "N字量縮洗盤":
		return "起漲長紅後窒息量守支撐買進 ； 跌破表態低點停損/過高賣出"
	default:
		return "指標觸發低檔訊號買進 ； 轉弱或超買訊號賣出"
	}
}

// FetchTop10FromCloud 從雲端抓取潛力股並嚴格核對真實日期
func (a *App) FetchTop10FromCloud() ([]Top10Stock, error) {
	csvURL := "https://raw.githubusercontent.com/Vincent2025-ops/Stockanalysis/main/finish/Stock_TOP10.csv"
	resp, err := httpGetWithRetry(csvURL)
	if err != nil { return nil, fmt.Errorf("網路連線失敗: %v", err) }
	defer resp.Body.Close()

	// 🎯 核心修復 1：計算今日應有的最新交易日 (平日即為今日，週末回溯至週五)
	now := time.Now()
	for now.Weekday() == time.Saturday {
		now = now.AddDate(0, 0, -1)
	}
	for now.Weekday() == time.Sunday {
		now = now.AddDate(0, 0, -2)
	}
	todayTradingDate := now.Format("2006-01-02")

	reader := csv.NewReader(resp.Body)
	records, err := reader.ReadAll()
	if err != nil { return nil, fmt.Errorf("CSV 解析失敗: %v", err) }

	// 支援多種日期格式 (YYYY-MM-DD, YYYY/MM/DD, YYYY/M/D 等) 並統一正規化
	parseAnyDate := func(raw string) string {
		raw = strings.TrimSpace(raw)
		raw = strings.ReplaceAll(raw, "/", "-")
		re := regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2})`)
		if match := re.FindStringSubmatch(raw); len(match) == 4 {
			y, _ := strconv.Atoi(match[1])
			m, _ := strconv.Atoi(match[2])
			d, _ := strconv.Atoi(match[3])
			if y > 1900 && m >= 1 && m <= 12 && d >= 1 && d <= 31 {
				return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
			}
		}
		return ""
	}

	// 🎯 核心修復 2：優先從 CSV 每一列的第 8 欄 (Index 7) 精準讀取實體資料日期
	syncDate := ""
	for i := 1; i < len(records); i++ {
		if len(records[i]) >= 8 {
			if pd := parseAnyDate(records[i][7]); pd != "" {
				syncDate = pd
				break
			}
		}
	}

	// 備援：若第 8 欄無日期，自第 7 欄說明字串字尾提取日期 (如 ... 2026-09-24)
	if syncDate == "" {
		for i := 1; i < len(records); i++ {
			if len(records[i]) >= 7 {
				if pd := parseAnyDate(records[i][6]); pd != "" {
					syncDate = pd
					break
				}
			}
		}
	}

	// 備援：若 CSV 完全無任何日期標記，才以 GitHub Commit 時間作參考
	if syncDate == "" {
		commitURL := "https://api.github.com/repos/Vincent2025-ops/Stockanalysis/commits?path=finish/Stock_TOP10.csv&page=1&per_page=1"
		req, _ := http.NewRequest("GET", commitURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		client := &http.Client{Timeout: 4 * time.Second}
		if cResp, cErr := client.Do(req); cErr == nil && cResp.StatusCode == 200 {
			var commitData []map[string]interface{}
			if json.NewDecoder(cResp.Body).Decode(&commitData) == nil && len(commitData) > 0 {
				if commitObj, ok := commitData[0]["commit"].(map[string]interface{}); ok {
					if committer, ok := commitObj["committer"].(map[string]interface{}); ok {
						if dateStr, ok := committer["date"].(string); ok {
							if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
								loc := time.FixedZone("CST", 8*3600)
								syncDate = t.In(loc).Format("2006-01-02")
							}
						}
					}
				}
			}
			cResp.Body.Close()
		}
	}

	if syncDate == "" {
		syncDate = "歷史存檔(日期未標註)"
	}

	// 🎯 判定雲端資料是否已更新至今日最新交易日
	isUpToDate := (syncDate == todayTradingDate)

	twseMap := make(map[string]string)
	respTWSE, errTWSE := httpGetWithRetry("https://www.twse.com.tw/exchangeReport/STOCK_DAY_ALL?response=json")
	if errTWSE == nil {
		defer respTWSE.Body.Close()
		var result map[string]interface{}
		if json.NewDecoder(respTWSE.Body).Decode(&result) == nil {
			if dataList, ok := result["data"].([]interface{}); ok {
				for _, item := range dataList {
					row := item.([]interface{})
					id := fmt.Sprintf("%v", row[0])
					change := strings.TrimSpace(fmt.Sprintf("%v", row[8]))
					twseMap[id] = change
				}
			}
		}
	}

	grouped := make(map[string][]sortableStock)
	for i := 1; i < len(records); i++ {
		row := records[i]
		if len(row) >= 7 {
			id := strings.TrimSpace(row[1])
			volStr := strings.ReplaceAll(strings.TrimSpace(row[4]), ",", "")
			vol, _ := strconv.ParseFloat(volStr, 64)
			if vol < 500000 { continue }

			change := twseMap[id] 
			indicator := strings.TrimSpace(row[0])

			// 數值清洗：移除「倍」與「%」，確保能精準轉為浮點數以進行排序
			valStr := strings.TrimSpace(row[5])
			valStr = strings.ReplaceAll(valStr, "倍", "")
			valStr = strings.ReplaceAll(valStr, "%", "")
			valStr = strings.TrimSpace(valStr)
			val, _ := strconv.ParseFloat(valStr, 64)

			desc := ""
			if len(row) > 6 {
				var parts []string
				for k := 6; k < len(row); k++ {
					if k == 7 && len(parts) > 0 { continue }
					p := strings.TrimSpace(row[k])
					if p != "" { parts = append(parts, p) }
				}
				desc = strings.Join(parts, " ")
			}

			desc = strings.ReplaceAll(desc, "10 日均量比值大於 0.5 且三大法人買超 ；小於 0.5 時平倉", "10 日均量比值>1.5 買進 ； 跌破 10MA 或 均量比>2.0 且為實體長黑 K 平倉")
			desc = strings.ReplaceAll(desc, "10 日均量比值大於 0.5 且三大法人買超", "10 日均量比值>1.5 買進")
			desc = strings.ReplaceAll(desc, "且三大法人買超 且小於 0.5 時平倉", " ；小於 0.5 時平倉")
			desc = strings.ReplaceAll(desc, "目前指標數值", "指標數值")

			if desc == "" {
				switch indicator {
				case "RSI": desc = "RSI 處於極低檔，具有極高超賣反彈爆發潛力"
				case "KD": desc = "KD 處於低檔超賣區，具備低檔黃金交叉佈局契機"
				case "MACD": desc = "MACD 柱狀圖動能強勁，處於多頭強勢上升趨勢"
				case "SMA": desc = "股價強勢突破均線表態，技術面全面轉強"
				case "BollingerBands", "Bollinger Bands", "Bollinger": desc = "股價觸及布林通道下軌，具備潛在的反彈與回歸均值動能"
				case "Momentum": desc = "動能指標急遽上升，市場買盤正積極湧入"
				case "VolumeRatio", "Volume Ratio", "ChipRatio", "成交量均量比", "成交量均量比策略（Volume Ratio）": desc = "10 日均量比值>1.5 買進 ； 跌破 10MA 或 均量比>2.0 且為實體長黑 K 平倉"
				default: desc = fmt.Sprintf("指標數值: %.2f", val)
				}
			}

			// 統一內部指標名稱為 VolumeRatio
			if indicator == "ChipRatio" || indicator == "Volume Ratio" || indicator == "VolumeRatio" || indicator == "成交量均量比" || indicator == "成交量均量比策略（Volume Ratio）" {
				indicator = "VolumeRatio"
			}

			days := 999
			reDays := regexp.MustCompile(`(?:已)?連續(?:符合)?\s*(\d+)\s*日`)
			if match := reDays.FindStringSubmatch(desc); len(match) > 1 {
				if d, err := strconv.Atoi(match[1]); err == nil && d > 0 {
					days = d
				}
			}

			summary := ""
			metricText := ""
			daysText := ""

			reMetric := regexp.MustCompile(`指標數值[:：]?\s*[-+]?[0-9]*\.?[0-9]+(?:\s*倍|%)?`)
			if m := reMetric.FindString(desc); m != "" {
				metricText = strings.TrimSpace(m)
			} else {
				metricText = fmt.Sprintf("指標數值: %.2f", val)
			}

			metricText = strings.ReplaceAll(metricText, "目前指標數值", "指標數值")

			if days < 999 {
				reDaysStr := regexp.MustCompile(`(?:已)?連續(?:符合)?\s*\d+\s*日`)
				if dStr := reDaysStr.FindString(desc); dStr != "" {
					daysText = strings.TrimSpace(dStr)
				} else {
					daysText = fmt.Sprintf("已連續符合 %d 日", days)
				}
				summary = metricText + "，" + daysText
			} else {
				summary = metricText
			}

			stock := sortableStock{
				Top10Stock: Top10Stock{
					Indicator:  indicator, 
					ID:         id, 
					Name:       strings.TrimSpace(row[2]), 
					Price:      strings.TrimSpace(row[3]), 
					Change:     change, 
					Feature:    desc, 
					Date:       syncDate,
					Days:       days,
					Summary:    summary,
					Rule:       getIndicatorRule(indicator),
					MarketDate: todayTradingDate,
					IsUpToDate: isUpToDate,
				}, 
				Value: val,
				Days:  days,
			}
			grouped[indicator] = append(grouped[indicator], stock)
		}
	}

	var finalResults []Top10Stock
	order := []string{"RSI", "KD", "MACD", "SMA", "BollingerBands", "Bollinger Bands", "Bollinger", "Momentum", "VolumeRatio"} 

	for _, ind := range order {
		stocks, ok := grouped[ind]
		if !ok || len(stocks) == 0 { continue }
		
		sort.Slice(stocks, func(i, j int) bool {
			if stocks[i].Days != stocks[j].Days {
				return stocks[i].Days < stocks[j].Days
			}
			if ind == "RSI" || ind == "KD" || strings.Contains(ind, "Bollinger") { 
				return stocks[i].Value < stocks[j].Value 
			}
			return stocks[i].Value > stocks[j].Value 
		})
		
		limit := 10
		if len(stocks) < limit { limit = len(stocks) }
		for _, s := range stocks[:limit] { finalResults = append(finalResults, s.Top10Stock) }
	}
	return finalResults, nil
}

func (a *App) ExportMarketData() (string, error) {
	resp, err := httpGetWithRetry("https://www.twse.com.tw/exchangeReport/STOCK_DAY_ALL?response=json")
	if err != nil { return "", err }
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	dataList, ok := result["data"].([]interface{})
	if !ok { return "", fmt.Errorf("API 回傳格式錯誤") }

	filename := fmt.Sprintf("Stock_AllPrice_%s.csv", getBaseTradingDate())
	file, _ := os.Create(filename)
	defer file.Close()
	file.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(file)
	defer writer.Flush()

	writer.Write([]string{"股票代號", "名稱", "成交量", "成交金額", "開盤價", "最高價", "最低價", "收盤價", "漲跌價"})
	count := 0
	for _, item := range dataList {
		row := item.([]interface{})
		if len(row) >= 9 {
			csvRow := make([]string, 9)
			for i := 0; i < 9; i++ { csvRow[i] = fmt.Sprintf("%v", row[i]) }
			writer.Write(csvRow)
			count++
		}
	}
	return fmt.Sprintf("✅ 成功匯出 %d 檔股票至 %s", count, filename), nil
}

// =========================================================
// ✨ N 字型量縮突破策略
// =========================================================

func (a *App) FetchNBreakout() ([]Top10Stock, error) {
	type stockCandidate struct {
		ID         string
		Name       string
		Price      float64
		Change     string
		Volume     float64
		TradeValue float64
	}
	var candidates []stockCandidate

	type CompanyInfo struct {
		Industry    string
		MainProduct string
		Capital     float64 
	}
	companyInfoMap := make(map[string]CompanyInfo)

	if respTWSEInfo, err := httpGetWithRetry("https://openapi.twse.com.tw/v1/opendata/t187ap03_L"); err == nil {
		var data []map[string]interface{}
		body, _ := io.ReadAll(respTWSEInfo.Body)
		if json.Unmarshal(body, &data) == nil {
			for _, c := range data {
				id := fmt.Sprintf("%v", c["公司代號"])
				ind := fmt.Sprintf("%v", c["產業類別"])
				prod := fmt.Sprintf("%v", c["主要經營業務"])
				if prod == "<nil>" || prod == "" { prod = fmt.Sprintf("%v", c["主要業務"]) }
				
				capStr := fmt.Sprintf("%v", c["實收資本額"])
				capVal, _ := strconv.ParseFloat(strings.ReplaceAll(capStr, ",", ""), 64)
				
				companyInfoMap[id] = CompanyInfo{Industry: ind, MainProduct: prod, Capital: capVal}
			}
		}
		respTWSEInfo.Body.Close()
	}

	if respTPEXInfo, err := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/mopsfin_t187ap03_O"); err == nil {
		var data []map[string]interface{}
		body, _ := io.ReadAll(respTPEXInfo.Body)
		if json.Unmarshal(body, &data) == nil {
			for _, c := range data {
				id := fmt.Sprintf("%v", c["公司代號"])
				ind := fmt.Sprintf("%v", c["產業類別"])
				prod := fmt.Sprintf("%v", c["主要經營業務"])
				if prod == "<nil>" || prod == "" { prod = fmt.Sprintf("%v", c["主要業務"]) }
				
				capStr := fmt.Sprintf("%v", c["實收資本額"])
				capVal, _ := strconv.ParseFloat(strings.ReplaceAll(capStr, ",", ""), 64)
				
				companyInfoMap[id] = CompanyInfo{Industry: ind, MainProduct: prod, Capital: capVal}
			}
		}
		respTPEXInfo.Body.Close()
	}

	respTWSE, errTWSE := httpGetWithRetry("https://openapi.twse.com.tw/v1/exchangeReport/STOCK_DAY_ALL")
	if errTWSE == nil {
		var dataList []map[string]interface{}
		body, _ := io.ReadAll(respTWSE.Body)
		if json.Unmarshal(body, &dataList) == nil && len(dataList) > 0 {
			for _, item := range dataList {
				id := strings.TrimSpace(fmt.Sprintf("%v", item["Code"]))
				name := strings.TrimSpace(fmt.Sprintf("%v", item["Name"]))
				vol, _ := strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", item["TradeVolume"]), ",", ""), 64)
				price, _ := strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", item["ClosingPrice"]), ",", ""), 64)
				
				if vol >= 200000 && price >= 10 && len(id) == 4 && !strings.HasPrefix(id, "00") { 
					candidates = append(candidates, stockCandidate{ID: id, Name: name, Price: price, Change: strings.TrimSpace(fmt.Sprintf("%v", item["Change"])), Volume: vol, TradeValue: vol * price})
				}
			}
		}
		respTWSE.Body.Close()
	}

	respOTC, errOTC := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/tpex_mainboard_quotes")
	if errOTC == nil {
		var tpexData []map[string]interface{}
		bodyOtc, _ := io.ReadAll(respOTC.Body)
		if json.Unmarshal(bodyOtc, &tpexData) == nil && len(tpexData) > 0 {
			for _, item := range tpexData {
				id := strings.TrimSpace(fmt.Sprintf("%v", item["SecuritiesCompanyCode"]))
				name := strings.TrimSpace(fmt.Sprintf("%v", item["CompanyName"]))
				vol, _ := strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", item["TradingVolume"]), ",", ""), 64)
				price, _ := strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", item["Close"]), ",", ""), 64)
				
				if vol >= 200000 && price >= 10 && len(id) == 4 && !strings.HasPrefix(id, "00") {
					candidates = append(candidates, stockCandidate{ID: id, Name: name, Price: price, Change: strings.TrimSpace(fmt.Sprintf("%v", item["Change"])), Volume: vol, TradeValue: vol * price})
				}
			}
		}
		respOTC.Body.Close()
	}

	if len(candidates) == 0 {
		for id := range companyInfoMap {
			if len(id) == 4 && !strings.HasPrefix(id, "00") {
				candidates = append(candidates, stockCandidate{
					ID:         id,
					Name:       "台股 " + id,
					Price:      50,
					Volume:     500000,
					TradeValue: 25000000, 
				})
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("初篩異常：無法從任何備援管道獲取名單，請確認網路連線是否正常。")
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].TradeValue > candidates[j].TradeValue })

	baseDateStr := getBaseTradingDate()
	now, _ := time.Parse("20060102", baseDateStr) // 🎯 修正：使用 := 短變數宣告，修復 undefined: now 錯誤
	now = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.Local)
	sevenMonthsAgo := now.AddDate(0, -7, 0)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 10) 
	var mu sync.Mutex
	
	type nBreakoutResult struct {
		StockID, Name, Change, Feature string
		Price, Score float64
	}
	var breakoutStocks []nBreakoutResult
	var apiFailCount int32

	for _, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(cand stockCandidate) {
			defer wg.Done()
			defer func() { <-sem }()

			time.Sleep(100 * time.Millisecond)

			info, hasInfo := companyInfoMap[cand.ID]
			prodDesc := ""
			indDesc := "技術強勢股"
			
			if hasInfo {
				if info.Capital > 0 && info.Capital > 3000000000 {
					return
				}
				if info.Industry != "" && info.Industry != "<nil>" && info.Industry != "無" {
					indDesc = info.Industry
				}
				if info.MainProduct != "" && info.MainProduct != "<nil>" && info.MainProduct != "無" {
					prodDesc = "主力產品：" + info.MainProduct + "。"
				} else if indDesc != "技術強勢股" {
					prodDesc = "深耕" + indDesc + "領域。"
				} else {
					prodDesc = "籌碼與線型結構優異。"
				}
			} else {
				prodDesc = "籌碼與線型結構優異。"
			}

			records, err := downloadYahooFinance(cand.ID, sevenMonthsAgo.Unix(), now.Unix())
			if err != nil || len(records) < 120 { 
				atomic.AddInt32(&apiFailCount, 1)
				return 
			}

			var opens, highs, lows, closes, vols []float64
			for i := 1; i < len(records); i++ {
				row := records[i]
				if len(row) < 7 || row[1] == "null" { continue } 
				o, _ := strconv.ParseFloat(row[1], 64)
				h, _ := strconv.ParseFloat(row[2], 64)
				l, _ := strconv.ParseFloat(row[3], 64)
				closeP, _ := strconv.ParseFloat(row[4], 64)
				v, _ := strconv.ParseFloat(row[6], 64)
				opens = append(opens, o)
				highs = append(highs, h)
				lows = append(lows, l)
				closes = append(closes, closeP)
				vols = append(vols, v)
			}

			n := len(closes)
			if n < 120 { return }

			if vols[n-1] == 0 && n > 3 {
				n = n - 1
			}

			todayClose, todayVol := closes[n-1], vols[n-1]
			yesterdayVol := vols[n-2]

			sum120Today, sum120Yest := 0.0, 0.0
			for i := n - 120; i < n; i++ { sum120Today += closes[i] }
			for i := n - 121; i < n - 1; i++ { sum120Yest += closes[i] }
			ma120Today, ma120Yest := sum120Today/120.0, sum120Yest/120.0
			if todayClose < ma120Today && ma120Today <= ma120Yest { return }

			sum60 := 0.0
			for i := n - 60; i < n; i++ { sum60 += closes[i] }
			ma60 := sum60 / 60.0
			bias60 := (todayClose - ma60) / ma60
			if bias60 < -0.15 || bias60 > 0.50 { return }

			ignitionFound := false
			var ignitionVol, ignitionLow, ignitionHigh float64
			var ignitionIdx int
			searchStart := n - 40
			if searchStart < 6 { searchStart = 6 }
			
			var maxVol float64
			for i := searchStart; i <= n-2; i++ { 
				sumV5 := 0.0
				for j := i - 5; j < i; j++ { sumV5 += vols[j] }
				avgV5 := sumV5 / 5.0
				if avgV5 == 0 { continue }
				
				gain := (closes[i] - closes[i-1]) / closes[i-1]
				isRedK := closes[i] > opens[i]
				
				if isRedK && gain >= 0.035 && vols[i] >= 1.5*avgV5 {
					if vols[i] > maxVol {
						maxVol = vols[i]
						ignitionFound = true
						ignitionVol, ignitionLow, ignitionHigh = vols[i], lows[i], highs[i]
						ignitionIdx = i
					}
				}
			}
			if !ignitionFound { return }
			
			if todayClose > ignitionHigh*1.05 { return }
			if n - 1 - ignitionIdx < 2 { return }

			washed := false
			for i := ignitionIdx + 1; i < n-1; i++ {
				if vols[i] <= 0.50 * ignitionVol {
					washed = true
					break
				}
			}
			if !washed { return }
			if todayVol == 0 { return }
			if todayClose < ignitionLow { return }

			score := 100.0
			distToHigh := (ignitionHigh - todayClose) / todayClose
			if distToHigh > 0 { 
				score -= distToHigh * 50 
			} else {
				score += 10
			}

			statusDesc := ""
			if todayVol > yesterdayVol * 1.5 && todayClose > opens[n-1] {
				score += 40
				statusDesc = "N字洗盤後剛出量發動"
			} else if todayVol <= 0.5 * ignitionVol {
				score += 30
				statusDesc = "N字極致量縮準備出量"
			} else {
				return
			}

			prodRunes := []rune(strings.ReplaceAll(prodDesc, "\n", ""))
			if len(prodRunes) > 22 { prodDesc = string(prodRunes[:22]) + "..." }

			feature := fmt.Sprintf("【%s】%s%s，守穩支撐待突破", indDesc, prodDesc, statusDesc)

			mu.Lock()
			breakoutStocks = append(breakoutStocks, nBreakoutResult{
				StockID: cand.ID, Name: cand.Name, Price: cand.Price, Change: cand.Change, Score: score, Feature: feature,
			})
			mu.Unlock()

		}(c)
	}

	wg.Wait()
	
	if apiFailCount > int32(len(candidates)*8/10) && len(candidates) > 0 {
		return nil, fmt.Errorf("遭到 Yahoo API 大規模阻擋限制 (連線失敗 %d/%d 檔)。請稍後再試或重啟網路更換 IP", apiFailCount, len(candidates))
	}

	sort.Slice(breakoutStocks, func(i, j int) bool { return breakoutStocks[i].Score > breakoutStocks[j].Score })

	results := []Top10Stock{}
	for i, b := range breakoutStocks {
		if i >= 10 { break }
		results = append(results, Top10Stock{ 
			Indicator:  "N字量縮洗盤", 
			ID:         b.StockID, 
			Name:       b.Name, 
			Price:      fmt.Sprintf("%.2f", b.Price), 
			Change:     b.Change, 
			Feature:    b.Feature, 
			Date:       baseDateStr,
			Days:       1,
			Summary:    "N字突破動能型態，已連續符合 1 日",
			Rule:       getIndicatorRule("N字量縮洗盤"),
			MarketDate: baseDateStr,
			IsUpToDate: true,
		})
	}

	var diagnosticMsg string
	if apiFailCount > 0 {
		diagnosticMsg = fmt.Sprintf("📊 掃描檢核：本次從全市場初步篩選出 %d 檔候選股，其中 %d 檔遭 Yahoo API 阻擋未測。其餘皆因未滿足 N 字嚴格型態遭淘汰。", len(candidates), apiFailCount)
	} else {
		diagnosticMsg = fmt.Sprintf("📊 掃描檢核：已成功針對全市場 %d 檔候選股進行深度運算，網路連線 100%% 成功！無標的代表目前市場無符合條件個股。", len(candidates))
	}

	if len(results) == 0 || len(results) < 10 {
		results = append(results, Top10Stock{
			Indicator:  "系統檢核報告", 
			ID:         "----", 
			Name:       "掃描完畢", 
			Price:      "-", 
			Change:     "-", 
			Feature:    diagnosticMsg,
			Date:       baseDateStr,
			Days:       0,
			Summary:    diagnosticMsg,
			Rule:       "-",
			MarketDate: baseDateStr,
			IsUpToDate: true,
		})
	}

	return results, nil
}

// =========================================================
// 分頁二：個股深度診斷
// =========================================================

type EPSDetail struct {
	Quarter string  `json:"q"`
	Value   float64 `json:"v"`
}

type BasicInfo struct {
	PE          string      `json:"pe"`
	PB          string      `json:"pb"`
	ROE         string      `json:"roe"`
	EPS         string      `json:"eps"`
	EpsQuarters string      `json:"epsQuarters"`
	EpsDetails  []EPSDetail `json:"epsDetails"`
	Assessment  string      `json:"assessment"`
}

type InstitutionalData struct {
	Foreign    int `json:"foreign"`
	Investment int `json:"investment"`
	Dealer     int `json:"dealer"`
	Total      int `json:"total"`
}

type MarginDetail struct {
	FinBuy            float64  `json:"finBuy"`
	FinSell           float64  `json:"finSell"`
	FinCashRepay      float64  `json:"finCashRepay"`
	FinPrevBalance    float64  `json:"finPrevBalance"`
	FinCurrentBalance float64  `json:"finCurrentBalance"`
	FinQuota          float64  `json:"finQuota"`
	FinUsage          float64  `json:"finUsage"`
	FinChangeRate     float64  `json:"finChangeRate"`
	SecBuy            float64  `json:"secBuy"`
	SecSell           float64  `json:"secSell"`
	SecStockRepay     float64  `json:"secPrevBalance"`
	SecPrevBalance    float64  `json:"secPrevBalance"`
	SecCurrentBalance float64  `json:"secCurrentBalance"`
	SecQuota          float64  `json:"secQuota"`
	SecUsage          float64  `json:"secUsage"`
	SecChangeRate     float64  `json:"secChangeRate"`
	FiveDayAvgShort   float64  `json:"fiveDayAvgShort"`
	MarginShortRatio  float64  `json:"marginShortRatio"`
	SqueezeForce      float64  `json:"squeezeForce"`
	ShortSqueezeStr   float64  `json:"shortSqueezeStr"`
	TrendEvals        []string `json:"trendEvals"`
	SqueezeEval       string   `json:"squeezeEval"`
	ShortSqueezeEval  string   `json:"shortSqueezeEval"`
}

type RetailData struct {
	DayTradeRatio        float64      `json:"dayTradeRatio"`
	MarginShortRatio     float64      `json:"marginShortRatio"`
	SqueezeForce         float64      `json:"squeezeForce"`
	ShortSqueezeStrength float64      `json:"shortSqueezeStrength"`
	Sentiment            string       `json:"sentiment"`
	Detail               MarginDetail `json:"detail"`
}

type ChipsData struct {
	MajorForceRatio float64 `json:"majorForceRatio"`
	Concentration   string  `json:"concentration"`
	TurnoverRate    float64 `json:"turnoverRate"`
}

type StockAnalysisResult struct {
	StockId       string            `json:"stockId"`
	StockName     string            `json:"stockName"`
	Date          string            `json:"date"`
	Trend         string            `json:"trend"`
	Basic         BasicInfo         `json:"basic"`
	Institutional InstitutionalData `json:"institutional"`
	Retail        RetailData        `json:"retail"`
	Chips         ChipsData         `json:"chips"`
}

func fetchQuarterlyEPS(stockID string) ([]string, []float64, error) {
	mirrors := []string{
		"fubon-ebrokerdj.fbs.com.tw",
		"djinfo.cathaysec.com.tw",
		"stock.capital.com.tw",
		"mdjmac.megasec.com.tw",
	}

	for _, mirror := range mirrors {
		url := fmt.Sprintf("https://%s/z/zc/zcq/zcq_%s.djhtm", mirror, stockID)
		resp, err := httpGetWithRetry(url)
		if err != nil { continue }
		
		reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
		utf8Data, err := io.ReadAll(reader)
		resp.Body.Close()
		if err != nil { continue }
		
		html := string(utf8Data)
		rows := strings.Split(strings.ToLower(html), "<tr")
		
		var rawQuarters []string
		var rawEps []string
		
		for _, row := range rows {
			cells := extractCells(row)
			if len(cells) < 2 { continue }
			
			if len(rawQuarters) == 0 {
				for i, c := range cells {
					if strings.Contains(c, "期別") || strings.Contains(c, "季別") {
						for j := i + 1; j < len(cells); j++ {
							rawQuarters = append(rawQuarters, strings.TrimSpace(cells[j]))
						}
						break
					}
				}
			}
			
			if len(rawQuarters) > 0 && len(rawEps) == 0 {
				for i, c := range cells {
					if strings.Contains(c, "每股盈餘") || strings.Contains(c, "每股稅後") || strings.Contains(strings.ToLower(c), "eps") {
						for j := i + 1; j < len(cells); j++ {
							rawEps = append(rawEps, strings.TrimSpace(cells[j]))
						}
						break
					}
				}
			}
			
			if len(rawQuarters) > 0 && len(rawEps) > 0 {
				break
			}
		}
		
		if len(rawQuarters) > 0 && len(rawEps) > 0 {
			minLen := len(rawQuarters)
			if len(rawEps) < minLen { minLen = len(rawEps) }
			
			var validQuarters []string
			var validEps []float64
			
			for i := 0; i < minLen; i++ {
				q := rawQuarters[i]
				epsStr := rawEps[i]
				if q == "" { continue }
				
				val, err := parseFinancialNumber(epsStr)
				if err != nil || math.Abs(val) < 0.0001 { 
					continue 
				}
				
				qUpper := strings.ToUpper(q)
				formattedQ := ""
				
				if strings.Contains(qUpper, "Q") {
					parts := strings.Split(qUpper, "Q")
					if len(parts) == 2 {
						y, _ := strconv.Atoi(parts[0])
						if y < 1000 { y += 1911 }
						formattedQ = fmt.Sprintf("%dQ%s", y, parts[1])
					} else {
						formattedQ = qUpper
					}
				} else if strings.Contains(q, ".") {
					parts := strings.Split(q, ".")
					if len(parts) == 2 {
						y, _ := strconv.Atoi(parts[0])
						if y < 1000 { y += 1911 }
						formattedQ = fmt.Sprintf("%dQ%s", y, parts[1])
					} else {
						formattedQ = q
					}
				} else {
					formattedQ = q
				}
				
				validQuarters = append(validQuarters, formattedQ)
				validEps = append(validEps, val)
			}
			
			if len(validQuarters) > 0 {
				var finalQ []string
				var finalEps []float64
				
				start := len(validQuarters) - 4
				if start < 0 { start = 0 }
				for i := start; i < len(validQuarters); i++ {
					finalQ = append(finalQ, validQuarters[i])
					finalEps = append(finalEps, validEps[i])
				}
				
				for i, j := 0, len(finalQ)-1; i < j; i, j = i+1, j-1 {
					finalQ[i], finalQ[j] = finalQ[j], finalQ[i]
					finalEps[i], finalEps[j] = finalEps[j], finalEps[i]
				}
				
				return finalQ, finalEps, nil
			}
		}
	}

	yahooAuthMu.Lock()
	crumb := yahooCrumb
	cookie := yahooCookie
	yahooAuthMu.Unlock()

	if crumb == "" {
		refreshYahooAuth()
		yahooAuthMu.Lock()
		crumb = yahooCrumb
		cookie = yahooCookie
		yahooAuthMu.Unlock()
	}

	client := &http.Client{Timeout: 5 * time.Second}
	suffixes := []string{".TW", ".TWO"}
	for _, suffix := range suffixes {
		urlStr := fmt.Sprintf("https://query1.finance.yahoo.com/v10/finance/quoteSummary/%s%s?modules=earnings", stockID, suffix)
		if crumb != "" { urlStr += "&crumb=" + crumb }
		
		req, _ := http.NewRequest("GET", urlStr, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		if cookie != "" { req.Header.Set("Cookie", cookie) }
		
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			var res map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&res) == nil {
				if qs, ok := res["quoteSummary"].(map[string]interface{}); ok {
					if resArr, ok := qs["result"].([]interface{}); ok && len(resArr) > 0 {
						if resObj, ok := resArr[0].(map[string]interface{}); ok {
							if earnings, ok := resObj["earnings"].(map[string]interface{}); ok {
								if ec, ok := earnings["earningsChart"].(map[string]interface{}); ok {
									if quarterly, ok := ec["quarterly"].([]interface{}); ok && len(quarterly) > 0 {
										var yQuarters []string
										var yEps []float64
										
										for _, qItem := range quarterly {
											qm, ok1 := qItem.(map[string]interface{})
											if !ok1 { continue }
											dateStr, _ := qm["date"].(string)
											
											var actualEps float64
											var okRaw bool

											actualObj, okActual := qm["actual"].(map[string]interface{})
											if okActual {
												actualEps, okRaw = actualObj["raw"].(float64)
												fmtStr, okFmt := actualObj["fmt"].(string)
												if okFmt && fmtStr != "" && fmtStr != "-" {
													if parsed, err := parseFinancialNumber(fmtStr); err == nil {
														if !okRaw || (math.Abs(actualEps) < 0.0001 && math.Abs(parsed) >= 0.0001) {
															actualEps = parsed
															okRaw = true
														}
													}
												}
											}

											if !okRaw || math.Abs(actualEps) < 0.0001 {
												continue
											}
											
											formattedDate := dateStr
											if len(dateStr) == 6 && (dateStr[1] == 'Q' || dateStr[1] == 'q') {
												formattedDate = dateStr[2:] + "Q" + string(dateStr[0])
											}
											
											yQuarters = append(yQuarters, formattedDate)
											yEps = append(yEps, actualEps)
										}
										
										if len(yQuarters) > 0 {
											for i, j := 0, len(yQuarters)-1; i < j; i, j = i+1, j-1 {
												yQuarters[i], yQuarters[j] = yQuarters[j], yQuarters[i]
												yEps[i], yEps[j] = yEps[j], yEps[i]
											}
											
											resp.Body.Close()
											return yQuarters, yEps, nil
										}
									}
								}
							}
						}
					}
				}
			}
			resp.Body.Close()
		} else if resp != nil {
			resp.Body.Close()
		}
	}

	return nil, nil, fmt.Errorf("查無EPS資料")
}

func (a *App) FetchStockAnalysis(input string) StockAnalysisResult {
	date := getBaseTradingDate()
	result := StockAnalysisResult{StockId: input, StockName: "查詢中...", Date: date, Trend: "中性"}

	stockID, price, name, err := resolveStock(input)
	if err != nil {
		result.StockName = "❌ 查無此股票"
		return result
	}

	result.StockId = stockID
	result.StockName = name

	var twsePE, twsePB float64
	var epsQuarters []string
	var epsVals []float64
	var errEps error
	
	var f, inv, d, totalInst int
	var dayTradeVol, totalVol, totalShares float64
	var marginDetail MarginDetail
	var errMargin error
	
	var superRatio, largeRatio float64
	var hasDiff bool

	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		resp, err := httpGetWithRetry("https://openapi.twse.com.tw/v1/exchangeReport/BWIBBU_ALL")
		if err == nil {
			defer resp.Body.Close()
			var data []map[string]interface{}
			body, _ := io.ReadAll(resp.Body)
			if json.Unmarshal(body, &data) == nil {
				for _, item := range data {
					if item["Code"] == stockID {
						twsePE, _ = strconv.ParseFloat(fmt.Sprintf("%v", item["PEratio"]), 64)
						twsePB, _ = strconv.ParseFloat(fmt.Sprintf("%v", item["PBratio"]), 64)
						return
					}
				}
			}
		}

		respOtc, errOtc := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/tpex_mainboard_perwibugf")
		if errOtc == nil {
			defer respOtc.Body.Close()
			var dataOtc []map[string]interface{}
			bodyOtc, _ := io.ReadAll(respOtc.Body)
			if json.Unmarshal(bodyOtc, &dataOtc) == nil {
				for _, item := range dataOtc {
					if item["SecuritiesCompanyCode"] == stockID {
						twsePE, _ = strconv.ParseFloat(fmt.Sprintf("%v", item["PriceEarningRatio"]), 64)
						twsePB, _ = strconv.ParseFloat(fmt.Sprintf("%v", item["PriceBookRatio"]), 64)
						return
					}
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		superRatio, largeRatio, hasDiff = fetchLargeShareholders(stockID)
	}()

	go func() {
		defer wg.Done()
		f, inv, d, totalInst = fetchInstitutional(stockID, date)
		dayTradeVol = fetchDayTradingForStock(stockID, date)
		totalVol = fetchDailyTotalVolume(stockID, date)
		totalShares = fetchTotalShares(stockID)
		marginDetail, errMargin = fetchAdvancedMarginData(stockID, date)
	}()
	
	go func() {
		defer wg.Done()
		epsQuarters, epsVals, errEps = fetchQuarterlyEPS(stockID)
	}()

	wg.Wait()

	result.Basic.EPS = "N/A"
	result.Basic.ROE = "N/A"
	result.Basic.PE = "N/A"
	result.Basic.PB = "N/A"

	_, peY, pbY, epsY := getYahooQuote(stockID)
	if twsePE == 0 { twsePE = peY }
	if twsePB == 0 { twsePB = pbY }
	
	if twsePB > 0 { result.Basic.PB = fmt.Sprintf("%.2f", twsePB) }
	
	if (errEps != nil || len(epsVals) == 0) && epsY != 0 {
		epsVals = []float64{}
		epsQuarters = []string{}
		errEps = nil
	}
	
	if errEps == nil {
		if len(epsVals) > 0 {
			sumEps := 0.0
			var epsDetails []EPSDetail
			for i := 0; i < len(epsVals); i++ {
				sumEps += epsVals[i]
				epsDetails = append(epsDetails, EPSDetail{Quarter: epsQuarters[i], Value: epsVals[i]})
			}
			
			result.Basic.EPS = fmt.Sprintf("%.2f", sumEps)
			result.Basic.EpsDetails = epsDetails
			
			if sumEps > 0 && price > 0 {
				realPE := price / sumEps
				result.Basic.PE = fmt.Sprintf("%.2f", realPE)
				if twsePB > 0 {
					netAssetValue := price / twsePB
					if netAssetValue > 0 {
						realROE := (sumEps / netAssetValue) * 100
						result.Basic.ROE = fmt.Sprintf("%.2f%%", realROE)
					}
				}
			} else if sumEps <= 0 {
				result.Basic.PE = "負值(虧損)"
				result.Basic.ROE = "虧損"
			}
		} else if epsY != 0 {
			result.Basic.EPS = fmt.Sprintf("%.2f", epsY)
			result.Basic.EpsQuarters = "(各季明細暫無資料)"
			
			if price > 0 && epsY > 0 {
				realPE := price / epsY
				result.Basic.PE = fmt.Sprintf("%.2f", realPE)
				if twsePB > 0 {
					netAssetValue := price / twsePB
					if netAssetValue > 0 {
						realROE := (epsY / netAssetValue) * 100
						result.Basic.ROE = fmt.Sprintf("%.2f%%", realROE)
					}
				}
			} else if epsY <= 0 {
				result.Basic.PE = "負值(虧損)"
				result.Basic.ROE = "虧損"
			}
		}
	} else {
		result.Basic.EpsQuarters = "(財報明細抓取失敗)"
		if twsePE > 0 && price > 0 {
			result.Basic.PE = fmt.Sprintf("%.2f", twsePE)
			inferredEPS := price / twsePE
			result.Basic.EPS = fmt.Sprintf("%.2f", inferredEPS)
			if twsePB > 0 {
				inferredROE := (inferredEPS / (price / twsePB)) * 100
				result.Basic.ROE = fmt.Sprintf("%.2f%%", inferredROE)
			}
		} else if twsePE == 0 && twsePB > 0 {
			result.Basic.PE, result.Basic.EPS, result.Basic.ROE = "負值(虧損)", "虧損", "虧損"
		}
	}

	evalPE, evalPB := 0.0, 0.0
	if result.Basic.PE != "N/A" && result.Basic.PE != "負值(虧損)" { evalPE, _ = strconv.ParseFloat(result.Basic.PE, 64) }
	if result.Basic.PB != "N/A" { evalPB, _ = strconv.ParseFloat(result.Basic.PB, 64) }
	
	if result.Basic.PE == "負值(虧損)" || evalPE < 0 {
		result.Basic.Assessment = "虧損狀態 (PE為負值，請參考 PB 與未來轉機)"
	} else if evalPE > 0 && evalPE < 15 && evalPB > 0 && evalPB < 1.5 {
		result.Basic.Assessment = "價值低估股 (PE<15, PB<1.5，落在便宜區間)"
	} else if evalPE > 0 && evalPE < 30 && strings.Contains(result.Basic.ROE, "15") {
		result.Basic.Assessment = "潛力成長股 (高ROE>15%，獲利能力優異)"
	} else if evalPE > 30 || evalPB > 3 {
		result.Basic.Assessment = "估值偏高 (PE>30 或 PB>3，留意追高風險)"
	} else if result.Basic.PE == "N/A" {
		result.Basic.Assessment = "目前財報/估值資訊不適用"
	} else {
		result.Basic.Assessment = "估值落在合理區間 (PE: 15~30, PB: 1.5~3)"
	}

	result.Institutional = InstitutionalData{Foreign: f, Investment: inv, Dealer: d, Total: totalInst}

	if totalVol > 0 {
		result.Retail.DayTradeRatio = (dayTradeVol / totalVol) * 100
		if totalShares > 0 { result.Chips.TurnoverRate = (totalVol / totalShares) * 100 }
	}

	if errMargin == nil {
		result.Retail.Detail = marginDetail
		result.Retail.MarginShortRatio = marginDetail.MarginShortRatio 
		result.Retail.SqueezeForce = marginDetail.SqueezeForce
		result.Retail.ShortSqueezeStrength = marginDetail.ShortSqueezeStr
		result.Retail.Sentiment = marginDetail.SqueezeEval
	} else {
		result.Retail.Detail = MarginDetail{
			TrendEvals: []string{fmt.Sprintf("⚠️ 查無資券資料: %v", errMargin)},
			SqueezeEval: "資料更新中或查無資券資料",
			ShortSqueezeEval: "資料更新中",
		}
		result.Retail.Sentiment = "資料更新中"
	}

	result.Chips.MajorForceRatio = largeRatio
	if largeRatio > 60 || superRatio > 40 {
		result.Chips.Concentration = "高度集中"
	} else if largeRatio > 40 {
		result.Chips.Concentration = "籌碼穩定"
	} else {
		result.Chips.Concentration = "籌碼渙散"
	}
	if !hasDiff { result.Chips.Concentration += " (單週)" }

	if totalInst > 2000 && result.Retail.SqueezeForce > 5 {
		result.Trend = "強烈偏多 (法人買+擠壓多方)"
	} else if totalInst < -2000 {
		result.Trend = "偏空 (法人大賣)"
	} else {
		result.Trend = "中性震盪"
	}

	return result
}

// =========================================================
// 分頁三：歷史回測 (Yahoo Finance JSON API 還原股價雙軌引擎)
// =========================================================

type TradeRecord struct {
	Date    string  `json:"date"`
	Action  string  `json:"action"`
	Price   float64 `json:"price"`
	Shares  float64 `json:"shares"`
	Capital float64 `json:"capital"`
	Profit  float64 `json:"profit"`
}

type BacktestResult struct {
	Strategy     string        `json:"strategy"`
	Description  string        `json:"description"`
	TotalReturn  float64       `json:"totalReturn"`
	MaxDrawdown  float64       `json:"maxDrawdown"`
	WinRate      float64       `json:"winRate"`
	FinalCapital float64       `json:"finalCapital"`
	Trades       []TradeRecord `json:"trades"`
}

type BacktestSummary struct {
	StockId   string           `json:"stockId"`
	StockName string           `json:"stockName"`
	Labels    []string         `json:"labels"`
	Prices    []float64        `json:"prices"`
	Results   []BacktestResult `json:"results"`
}

var (
	yahooCookie string
	yahooCrumb  string
	yahooAuthMu sync.Mutex

	tdccDownloadFlag int32 
)

func downloadTDCCRobust(fileName string) bool {
	for attempt := 1; attempt <= 3; attempt++ {
		client := &http.Client{
			Timeout:   10 * time.Minute,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				DisableKeepAlives: false,
			},
		}
		req, _ := http.NewRequest("GET", "https://smart.tdcc.com.tw/opendata/getOD.ashx?id=1-5", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,en-US;q=0.8,en;q=0.7")
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("Upgrade-Insecure-Requests", "1")

		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			out, errCreate := os.Create(fileName + ".tmp")
			if errCreate == nil {
				buf := make([]byte, 1024*1024)
				copiedBytes, copyErr := io.CopyBuffer(out, resp.Body, buf)
				out.Close()
				resp.Body.Close()

				if copyErr == nil && copiedBytes > 10000000 {
					os.Rename(fileName+".tmp", fileName)
					fmt.Printf("[System] ✅ TDCC 集保檔案下載成功！大小: %.2f MB\n", float64(copiedBytes)/1024/1024)
					return true
				} else {
					fmt.Printf("[System] ⚠️ TDCC 下載中斷或檔案過小 (%.2f MB，錯誤: %v)，準備重試...\n", float64(copiedBytes)/1024/1024, copyErr)
				}
			} else {
				resp.Body.Close()
			}
		} else {
			if resp != nil { resp.Body.Close() }
			fmt.Printf("[System] ❌ TDCC 下載請求失敗，重試 %d/3...\n", attempt)
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

func refreshYahooAuth() {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	
	req, _ := http.NewRequest("GET", "https://fc.yahoo.com", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil { return }
	
	var cookies []string
	for _, c := range resp.Cookies() {
		cookies = append(cookies, c.String())
	}
	cookieStr := strings.Join(cookies, "; ")
	resp.Body.Close()

	req, _ = http.NewRequest("GET", "https://query1.finance.yahoo.com/v1/test/getcrumb", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", cookieStr)
	resp, err = client.Do(req)
	if err == nil && resp.StatusCode == 200 {
		b, _ := io.ReadAll(resp.Body)
		
		yahooAuthMu.Lock()
		yahooCookie = cookieStr
		yahooCrumb = string(b)
		yahooAuthMu.Unlock()
		
		resp.Body.Close()
	}
}

func downloadYahooFinance(stockID string, period1, period2 int64) ([][]string, error) {
	yahooAuthMu.Lock()
	crumb := yahooCrumb
	cookie := yahooCookie
	yahooAuthMu.Unlock()

	if crumb == "" {
		refreshYahooAuth()
		yahooAuthMu.Lock()
		crumb = yahooCrumb
		cookie = yahooCookie
		yahooAuthMu.Unlock()
	}

	suffixes := []string{".TW", ".TWO"}
	client := &http.Client{Timeout: 10 * time.Second}

	for _, suffix := range suffixes {
		urlStr := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s%s?range=2y&interval=1d", stockID, suffix)
		if crumb != "" {
			urlStr += "&crumb=" + crumb
		}
		
		req, _ := http.NewRequest("GET", urlStr, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "application/json")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			
			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil { continue }
			
			chart, ok := result["chart"].(map[string]interface{})
			if !ok { continue }
			resArray, ok := chart["result"].([]interface{})
			if !ok || len(resArray) == 0 || resArray[0] == nil { continue }
			
			firstResult := resArray[0].(map[string]interface{})
			timestamps, ok1 := firstResult["timestamp"].([]interface{})
			indicators, ok2 := firstResult["indicators"].(map[string]interface{})
			if !ok1 || !ok2 { continue }
			
			quoteArray, ok3 := indicators["quote"].([]interface{})
			adjCloseArray, ok4 := indicators["adjclose"].([]interface{})
			if !ok3 || !ok4 || len(quoteArray) == 0 || len(adjCloseArray) == 0 { continue }
			
			quote, ok5 := quoteArray[0].(map[string]interface{})
			adjCloseObj, ok6 := adjCloseArray[0].(map[string]interface{})
			if !ok5 || !ok6 { continue }
			
			opens, _ := quote["open"].([]interface{})
			highs, _ := quote["high"].([]interface{})
			lows, _ := quote["low"].([]interface{})
			closes, _ := quote["close"].([]interface{})
			volumes, _ := quote["volume"].([]interface{})
			adjCloses, _ := adjCloseObj["adjclose"].([]interface{})
			
			if len(timestamps) != len(opens) { continue }
			
			var records [][]string
			records = append(records, []string{"Date", "Open", "High", "Low", "Close", "Adj Close", "Volume"})
			
			for i := 0; i < len(timestamps); i++ {
				if i >= len(opens) || i >= len(highs) || i >= len(lows) || i >= len(closes) || i >= len(adjCloses) || i >= len(volumes) { break }
				if opens[i] == nil || highs[i] == nil || lows[i] == nil || closes[i] == nil || adjCloses[i] == nil || volumes[i] == nil { continue }
				
				t := int64(timestamps[i].(float64))
				dateStr := time.Unix(t, 0).Format("2006-01-02")
				
				records = append(records, []string{
					dateStr,
					fmt.Sprintf("%f", opens[i].(float64)),
					fmt.Sprintf("%f", highs[i].(float64)),
					fmt.Sprintf("%f", lows[i].(float64)),
					fmt.Sprintf("%f", closes[i].(float64)),
					fmt.Sprintf("%f", adjCloses[i].(float64)),
					fmt.Sprintf("%f", volumes[i].(float64)),
				})
			}
			if len(records) > 1 { return records, nil }
		} else if resp != nil {
			if resp.StatusCode == 401 || resp.StatusCode == 429 {
				yahooAuthMu.Lock()
				yahooCrumb = ""
				yahooAuthMu.Unlock()
			}
			resp.Body.Close()
		}
	}
	return nil, fmt.Errorf("Yahoo Finance 拒絕連線或無資料")
}

func (a *App) DownloadStockHistory(input string) (string, error) {
	stockID, _, _, err := resolveStock(input)
	if err != nil { return "", fmt.Errorf("查無此股票，請確認輸入正確名稱或代號") }

	filename := fmt.Sprintf("%s_stock_data.csv", stockID)
	file, err := os.Create(filename)
	if err != nil { return "", err }
	defer file.Close()
	
	file.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(file)
	defer writer.Flush()
	
	writer.Write([]string{"Date", "Open", "High", "Low", "Close", "Volume"})
	
	baseDateStr := getBaseTradingDate()
	now, _ := time.Parse("20060102", baseDateStr)
	now = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.Local)
	oneYearAgo := now.AddDate(-1, 0, 0)
	
	records, errYahoo := downloadYahooFinance(stockID, oneYearAgo.Unix(), now.Unix())
	if errYahoo == nil {
		totalRecords := 0
		for i, row := range records {
			if i == 0 || len(row) < 7 || row[1] == "null" { continue }
			dateStr := row[0]
			open, _ := strconv.ParseFloat(row[1], 64)
			high, _ := strconv.ParseFloat(row[2], 64)
			low, _ := strconv.ParseFloat(row[3], 64)
			closeP, _ := strconv.ParseFloat(row[4], 64)
			adjClose, _ := strconv.ParseFloat(row[5], 64)
			vol, _ := strconv.ParseFloat(row[6], 64)

			ratio := 1.0
			if closeP > 0 { ratio = adjClose / closeP }

			writer.Write([]string{
				dateStr,
				fmt.Sprintf("%.2f", open*ratio),
				fmt.Sprintf("%.2f", high*ratio),
				fmt.Sprintf("%.2f", low*ratio),
				fmt.Sprintf("%.2f", adjClose),
				fmt.Sprintf("%.0f", vol),
			})
			totalRecords++
		}
		return fmt.Sprintf("✅ 成功從 Yahoo 下載 %d 筆【還原】歷史資料至 %s", totalRecords, filename), nil
	}

	totalRecords := 0
	for i := 11; i >= 0; i-- { 
		targetMonth := now.AddDate(0, -i, 0).Format("20060102")
		url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/STOCK_DAY?response=json&date=%s&stockNo=%s", targetMonth, stockID)
		
		resp, err := httpGetWithRetry(url)
		if err == nil {
			var result map[string]interface{}
			json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if dataList, ok := result["data"].([]interface{}); ok {
				for _, item := range dataList {
					row := item.([]interface{})
					if len(row) < 9 { continue }
					
					twseDate := fmt.Sprintf("%v", row[0]) 
					parts := strings.Split(twseDate, "/")
					dateStr := ""
					if len(parts) == 3 {
						y, _ := strconv.Atoi(parts[0])
						m, _ := strconv.Atoi(parts[1])
						d, _ := strconv.Atoi(parts[2])
						dateStr = fmt.Sprintf("%04d-%02d-%02d", y+1911, m, d)
					}
					
					volStr := strings.ReplaceAll(fmt.Sprintf("%v", row[1]), ",", "")
					openStr := strings.ReplaceAll(fmt.Sprintf("%v", row[3]), ",", "")
					highStr := strings.ReplaceAll(fmt.Sprintf("%v", row[4]), ",", "")
					lowStr := strings.ReplaceAll(fmt.Sprintf("%v", row[5]), ",", "")
					closeStr := strings.ReplaceAll(fmt.Sprintf("%v", row[6]), ",", "")
					
					openStr = strings.ReplaceAll(openStr, "X", "")
					highStr = strings.ReplaceAll(highStr, "X", "")
					lowStr = strings.ReplaceAll(lowStr, "X", "")
					closeStr = strings.ReplaceAll(closeStr, "X", "")

					writer.Write([]string{dateStr, openStr, highStr, lowStr, closeStr, volStr})
					totalRecords++
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	
	if totalRecords > 0 { return fmt.Sprintf("⚠️ Yahoo 連線失敗，退回 TWSE 下載 %d 筆【未還原】資料至 %s", totalRecords, filename), nil }
	return "", fmt.Errorf("下載失敗：Yahoo 與證交所皆無回應，請確認網路連線")
}

// 🎯 核心回測引擎：加入開盤價與成交量解析，支援均量比策略精準運算
func (a *App) RunBacktest(input string, months int) (BacktestSummary, error) {
	var emptySummary BacktestSummary
	stockID, _, stockName, err := resolveStock(input)
	if err != nil { return emptySummary, fmt.Errorf("查無此股票，請確認輸入正確名稱或代號") }

	filename := fmt.Sprintf("%s_stock_data.csv", stockID)
	if _, err := os.Stat(filename); os.IsNotExist(err) { return emptySummary, fmt.Errorf("請先下載資料") }
	
	file, _ := os.Open(filename)
	defer file.Close()
	reader := csv.NewReader(file)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1

	rows, _ := reader.ReadAll()

	var opens, highs, lows, prices, volumes []float64 
	var dates []time.Time
	var rawDates []string

	for i, row := range rows {
		if i == 0 || len(row) < 6 { continue }
		dateStr := row[0]
		o, _ := strconv.ParseFloat(row[1], 64)
		h, _ := strconv.ParseFloat(row[2], 64)
		l, _ := strconv.ParseFloat(row[3], 64)
		p, _ := strconv.ParseFloat(row[4], 64)
		v, _ := strconv.ParseFloat(row[5], 64)
		
		if p > 0 {
			parsedDate, _ := time.Parse("2006-01-02", dateStr)
			opens = append(opens, o)
			highs = append(highs, h)
			lows = append(lows, l)
			prices = append(prices, p)
			volumes = append(volumes, v)
			dates = append(dates, parsedDate)
			rawDates = append(rawDates, dateStr)
		}
	}
	
	if len(prices) < 30 { return emptySummary, fmt.Errorf("有效交易天數僅 %d 天，數據不足以進行回測", len(prices)) }

	startIndex := 0
	if months > 0 && months < 12 && len(dates) > 0 {
		latestDate := dates[len(dates)-1]
		targetDate := latestDate.AddDate(0, -months, 0)
		for i, d := range dates {
			if !d.IsZero() && (d.After(targetDate) || d.Equal(targetDate)) {
				startIndex = i
				break
			}
		}
	}

	if startIndex >= len(prices) { startIndex = 0 }
	
	evalPrices := prices[startIndex:]
	evalDates := rawDates[startIndex:]
	
	if len(evalPrices) < 5 { return emptySummary, fmt.Errorf("所選時間區間內數據不足 (%d 天)", len(evalPrices)) }

	var results []BacktestResult
	results = append(results, simulateStrategy("RSI 策略 (<30買, >70賣)", "指標跌破 30 時超賣買進，突破 70 時超買平倉", evalDates, evalPrices, runRSILogic(prices)[startIndex:]))
	results = append(results, simulateStrategy("SMA 策略 (突破月線買)", "收盤價突破 20 日均線(月線)時買進，跌破時平倉", evalDates, evalPrices, runSMALogic(prices)[startIndex:]))
	results = append(results, simulateStrategy("MACD 策略 (黃金交叉買)", "MACD 柱狀圖由負轉正(黃金交叉)時買進，由正轉負時平倉", evalDates, evalPrices, runMACDLogic(prices)[startIndex:]))
	results = append(results, simulateStrategy("KD 策略 (低檔黃金交叉買)", "K、D 皆小於 30 且 K 突破 D 時買進，皆大於 70 且 K 跌破 D 時平倉", evalDates, evalPrices, runKDLogic(prices, highs, lows)[startIndex:]))
	results = append(results, simulateStrategy("Bollinger Bands (跌破下軌買)", "收盤價跌破布林通道下軌時買進，突破上軌時平倉", evalDates, evalPrices, runBollingerLogic(prices)[startIndex:]))
	results = append(results, simulateStrategy("Momentum 動能策略", "10 日動能指標由負轉正時買進，由正轉負時平倉", evalDates, evalPrices, runMomentumLogic(prices)[startIndex:]))
	results = append(results, simulateStrategy("成交量均量比策略（Volume Ratio）", "10 日均量比值大於 1.5 買進 ； 跌破 10MA 或 均量比大於 2.0 且為實體長黑 K 平倉", evalDates, evalPrices, runVolumeRatioLogic(prices, opens, volumes)[startIndex:]))
	
	summary := BacktestSummary{ StockId: stockID, StockName: stockName, Labels: evalDates, Prices: evalPrices, Results: results }
	return summary, nil
}

func getBaseTradingDate() string {
	resp, err := httpGetWithRetry("https://www.twse.com.tw/exchangeReport/MI_INDEX?response=json&type=IND")
	if err == nil {
		defer resp.Body.Close()
		var result map[string]interface{}
		if json.NewDecoder(resp.Body).Decode(&result) == nil {
			if dateStr, ok := result["date"].(string); ok && len(dateStr) == 8 {
				return dateStr 
			}
		}
	}

	resp2, err2 := httpGetWithRetry("https://openapi.twse.com.tw/v1/exchangeReport/FMTQIK")
	if err2 == nil {
		defer resp2.Body.Close()
		var data []map[string]interface{}
		body, _ := io.ReadAll(resp2.Body)
		if json.Unmarshal(body, &data) == nil && len(data) > 0 {
			lastItem := data[len(data)-1]
			twDateStr := fmt.Sprintf("%v", lastItem["Date"])
			if len(twDateStr) == 7 { 
				if y, err := strconv.Atoi(twDateStr[0:3]); err == nil {
					return fmt.Sprintf("%04d%s", y+1911, twDateStr[3:7])
				}
			}
		}
	} else if resp2 != nil {
		resp2.Body.Close()
	}

	now := time.Now()
	if now.Hour() < 15 { now = now.AddDate(0, 0, -1) }
	for now.Weekday() == time.Sunday || now.Weekday() == time.Saturday { now = now.AddDate(0, 0, -1) }
	return now.Format("20060102")
}

func previousDate(dateStr string) string {
	t, err := time.Parse("20060102", dateStr)
	if err != nil { return dateStr }
	t = t.AddDate(0, 0, -1)
	if t.Weekday() == time.Saturday { t = t.AddDate(0, 0, -1) }
	if t.Weekday() == time.Sunday { t = t.AddDate(0, 0, -2) }
	return t.Format("20060102")
}

func fetchPEAndPBFromTWSEOpenAPI(stockID string) (float64, float64, error) {
	resp, err := httpGetWithRetry("https://openapi.twse.com.tw/v1/exchangeReport/BWIBBU_ALL")
	if err == nil {
		defer resp.Body.Close()
		var data []map[string]interface{}
		body, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(body, &data) == nil {
			for _, item := range data {
				if item["Code"] == stockID {
					pe, _ := strconv.ParseFloat(fmt.Sprintf("%v", item["PEratio"]), 64)
					pb, _ := strconv.ParseFloat(fmt.Sprintf("%v", item["PBratio"]), 64)
					return pe, pb, nil
				}
			}
		}
	}

	respOtc, errOtc := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/tpex_mainboard_perwibugf")
	if errOtc == nil {
		defer respOtc.Body.Close()
		var dataOtc []map[string]interface{}
		bodyOtc, _ := io.ReadAll(respOtc.Body)
		if json.Unmarshal(bodyOtc, &dataOtc) == nil {
			for _, item := range dataOtc {
				if item["SecuritiesCompanyCode"] == stockID {
					pe, _ := strconv.ParseFloat(fmt.Sprintf("%v", item["PriceEarningRatio"]), 64)
					pb, _ := strconv.ParseFloat(fmt.Sprintf("%v", item["PriceBookRatio"]), 64)
					return pe, pb, nil
				}
			}
		}
	}
	
	return 0, 0, fmt.Errorf("OpenAPI 查無資料")
}

func extractTPExData(result map[string]interface{}) []interface{} {
	if aaData, ok := result["aaData"].([]interface{}); ok { return aaData }
	if data, ok := result["data"].([]interface{}); ok { return data }
	if tables, ok := result["tables"].([]interface{}); ok {
		for _, tb := range tables {
			if tableMap, ok := tb.(map[string]interface{}); ok {
				if data, ok := tableMap["data"].([]interface{}); ok { return data }
			}
		}
	}
	return nil
}

func fetchInstitutional(stockID, currentDate string) (int, int, int, int) {
	var finalF, finalInv, finalD, finalTotal int
	var found bool
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		date := currentDate
		for i := 0; i < 3; i++ {
			mu.Lock()
			isFound := found
			mu.Unlock()
			if isFound { return }

			url := fmt.Sprintf("https://www.twse.com.tw/fund/T86?response=json&date=%s&selectType=ALL", date)
			resp, err := httpGetWithRetry(url)
			if err == nil {
				var result map[string]interface{}
				json.NewDecoder(resp.Body).Decode(&result)
				resp.Body.Close()
				if dataList, ok := result["data"].([]interface{}); ok {
					for _, item := range dataList {
						row := item.([]interface{})
						if strings.TrimSpace(fmt.Sprintf("%v", row[0])) == stockID {
							f, _ := strconv.Atoi(strings.ReplaceAll(fmt.Sprintf("%v", row[4]), ",", ""))
							inv, _ := strconv.Atoi(strings.ReplaceAll(fmt.Sprintf("%v", row[7]), ",", ""))
							d, _ := strconv.Atoi(strings.ReplaceAll(fmt.Sprintf("%v", row[11]), ",", ""))
							
							mu.Lock()
							if !found {
								finalF, finalInv, finalD, finalTotal = f/1000, inv/1000, d/1000, (f+inv+d)/1000
								found = true
							}
							mu.Unlock()
							return
						}
					}
				}
			}
			date = previousDate(date)
		}
	}()

	go func() {
		defer wg.Done()
		date := currentDate
		for i := 0; i < 3; i++ {
			mu.Lock()
			isFound := found
			mu.Unlock()
			if isFound { return }

			y, _ := strconv.Atoi(date[0:4])
			rocDate := fmt.Sprintf("%d/%s/%s", y-1911, date[4:6], date[6:8])
			urlTPEx := fmt.Sprintf("https://www.tpex.org.tw/web/stock/3insti/daily_trade/3itrade_hedge_result.php?l=zh-tw&o=json&t=D&d=%s", rocDate)
			respTPEx, errTPEx := httpGetWithRetry(urlTPEx)
			if errTPEx == nil {
				var result map[string]interface{}
				if json.NewDecoder(respTPEx.Body).Decode(&result) == nil {
					if targetData := extractTPExData(result); targetData != nil {
						for _, item := range targetData {
							row := item.([]interface{})
							if len(row) >= 10 && fmt.Sprintf("%v", row[0]) == stockID {
								var f, inv, d int
								if len(row) >= 24 {
									fStr := strings.ReplaceAll(fmt.Sprintf("%v", row[10]), ",", "")
									f, _ = strconv.Atoi(fStr)
									iStr := strings.ReplaceAll(fmt.Sprintf("%v", row[13]), ",", "")
									inv, _ = strconv.Atoi(iStr)
									dStr := strings.ReplaceAll(fmt.Sprintf("%v", row[23]), ",", "")
									d, _ = strconv.Atoi(dStr)
								} else if len(row) >= 19 {
									fStr := strings.ReplaceAll(fmt.Sprintf("%v", row[10]), ",", "")
									f, _ = strconv.Atoi(fStr)
									iStr := strings.ReplaceAll(fmt.Sprintf("%v", row[13]), ",", "")
									inv, _ = strconv.Atoi(iStr)
									dStr := strings.ReplaceAll(fmt.Sprintf("%v", row[18]), ",", "")
									d, _ = strconv.Atoi(dStr)
								} else {
									fStr := strings.ReplaceAll(fmt.Sprintf("%v", row[4]), ",", "")
									f, _ = strconv.Atoi(fStr)
									iStr := strings.ReplaceAll(fmt.Sprintf("%v", row[7]), ",", "")
									inv, _ = strconv.Atoi(iStr)
									dStr := strings.ReplaceAll(fmt.Sprintf("%v", row[10]), ",", "")
									d, _ = strconv.Atoi(dStr)
								}
								
								mu.Lock()
								if !found {
									finalF, finalInv, finalD, finalTotal = f/1000, inv/1000, d/1000, (f+inv+d)/1000
									found = true
								}
								mu.Unlock()
								respTPEx.Body.Close()
								return
							}
						}
					}
				}
				respTPEx.Body.Close()
			}
			date = previousDate(date)
		}
	}()

	wg.Wait()
	return finalF, finalInv, finalD, finalTotal
}

func fetchDayTradingForStock(stockID, currentDate string) float64 {
	var finalVol float64
	var found bool
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		date := currentDate
		for i := 0; i < 3; i++ {
			mu.Lock()
			isFound := found
			mu.Unlock()
			if isFound { return }

			url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/TWTB4U?response=csv&date=%s", date)
			resp, err := httpGetWithRetry(url)
			if err == nil {
				utf8Reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
				utf8Data, _ := io.ReadAll(utf8Reader)
				resp.Body.Close()
				lines := strings.Split(string(utf8Data), "\n")
				reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
				reader.LazyQuotes = true
				reader.FieldsPerRecord = -1
				if records, err := reader.ReadAll(); err == nil {
					for _, fields := range records {
						if len(fields) >= 4 && strings.TrimSpace(strings.ReplaceAll(fields[0], "\"", "")) == stockID {
							raw := strings.ReplaceAll(fields[3], "\"", "")
							vol, _ := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
							mu.Lock()
							if !found {
								finalVol = vol
								found = true
							}
							mu.Unlock()
							return
						}
					}
				}
			}
			date = previousDate(date)
		}
	}()

	go func() {
		defer wg.Done()
		date := currentDate
		for i := 0; i < 3; i++ {
			mu.Lock()
			isFound := found
			mu.Unlock()
			if isFound { return }

			y, _ := strconv.Atoi(date[0:4])
			rocDate := fmt.Sprintf("%d/%s/%s", y-1911, date[4:6], date[6:8])
			urlTPEx := fmt.Sprintf("https://www.tpex.org.tw/web/stock/trading/intraday_stat/intraday_trading_stat_result.php?l=zh-tw&o=json&d=%s", rocDate)
			respTPEx, errTPEx := httpGetWithRetry(urlTPEx)
			if errTPEx == nil {
				var result map[string]interface{}
				if json.NewDecoder(respTPEx.Body).Decode(&result) == nil {
					if targetData := extractTPExData(result); targetData != nil {
						for _, item := range targetData {
							row := item.([]interface{})
							if len(row) >= 4 && fmt.Sprintf("%v", row[0]) == stockID {
								volStr := strings.ReplaceAll(fmt.Sprintf("%v", row[3]), ",", "")
								vol, _ := strconv.ParseFloat(volStr, 64)
								mu.Lock()
								if !found {
									finalVol = vol * 1000
									found = true
								}
								mu.Unlock()
								respTPEx.Body.Close()
								return
							}
						}
					}
				}
				respTPEx.Body.Close()
			}
			date = previousDate(date)
		}
	}()

	wg.Wait()
	return finalVol
}

func fetchDailyTotalVolume(stockID, currentDate string) float64 {
	yahooAuthMu.Lock()
	crumb := yahooCrumb
	cookie := yahooCookie
	yahooAuthMu.Unlock()

	if crumb == "" {
		refreshYahooAuth()
		yahooAuthMu.Lock()
		crumb = yahooCrumb
		cookie = yahooCookie
		yahooAuthMu.Unlock()
	}

	client := &http.Client{Timeout: 4 * time.Second}
	urlStr := fmt.Sprintf("https://query1.finance.yahoo.com/v7/finance/quote?symbols=%s.TW,%s.TWO", stockID, stockID)
	if crumb != "" { urlStr += "&crumb=" + crumb }
	
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if cookie != "" { req.Header.Set("Cookie", cookie) }
	
	respY, errY := client.Do(req)
	if errY == nil && respY.StatusCode == 200 {
		var res map[string]interface{}
		if json.NewDecoder(respY.Body).Decode(&res) == nil {
			if qr, ok := res["quoteResponse"].(map[string]interface{}); ok {
				if results, ok := qr["result"].([]interface{}); ok && len(results) > 0 {
					if data, ok := results[0].(map[string]interface{}); ok {
						if vol, ok := data["regularMarketVolume"].(float64); ok && vol > 0 {
							respY.Body.Close()
							return vol
						}
					}
				}
			}
		}
		respY.Body.Close()
	} else if respY != nil {
		respY.Body.Close()
	}

	queryMonth := currentDate[:6] + "01"
	url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/STOCK_DAY?response=json&date=%s&stockNo=%s", queryMonth, stockID)
	resp, err := httpGetWithRetry(url)
	if err == nil {
		defer resp.Body.Close()
		var result map[string]interface{}
		if json.NewDecoder(resp.Body).Decode(&result) == nil {
			if dataList, ok := result["data"].([]interface{}); ok && len(dataList) > 0 {
				for i := len(dataList) - 1; i >= 0; i-- {
					row, ok := dataList[i].([]interface{})
					if ok && len(row) >= 2 {
						volStr := strings.ReplaceAll(fmt.Sprintf("%v", row[1]), ",", "")
						vol, _ := strconv.ParseFloat(volStr, 64)
						return vol
					}
				}
			}
		}
	}

	y, _ := strconv.Atoi(currentDate[0:4])
	rocMonth := fmt.Sprintf("%d/%s", y-1911, currentDate[4:6])
	urlTPEx := fmt.Sprintf("https://www.tpex.org.tw/web/stock/aftertrading/daily_trading_info/st43_result.php?l=zh-tw&o=json&d=%s&stkno=%s", rocMonth, stockID)
	respTPEx, errTPEx := httpGetWithRetry(urlTPEx)
	if errTPEx == nil {
		defer respTPEx.Body.Close()
		var result map[string]interface{}
		if json.NewDecoder(respTPEx.Body).Decode(&result) == nil {
			if targetData := extractTPExData(result); targetData != nil && len(targetData) > 0 {
				for i := len(targetData) - 1; i >= 0; i-- {
					row, ok := targetData[i].([]interface{})
					if ok && len(row) >= 2 {
						volStr := strings.ReplaceAll(fmt.Sprintf("%v", row[1]), ",", "")
						vol, _ := strconv.ParseFloat(volStr, 64)
						return vol * 1000 
					}
				}
			}
		}
	}

	return 0
}

func fetchTotalShares(stockID string) float64 {
	yahooAuthMu.Lock()
	crumb := yahooCrumb
	cookie := yahooCookie
	yahooAuthMu.Unlock()

	if crumb == "" {
		refreshYahooAuth()
		yahooAuthMu.Lock()
		crumb = yahooCrumb
		cookie = yahooCookie
		yahooAuthMu.Unlock()
	}

	client := &http.Client{Timeout: 4 * time.Second}
	urlStr := fmt.Sprintf("https://query1.finance.yahoo.com/v7/finance/quote?symbols=%s.TW,%s.TWO", stockID, stockID)
	if crumb != "" {
		urlStr += "&crumb=" + crumb
	}
	
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	
	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == 200 {
		var res map[string]interface{}
		if json.NewDecoder(resp.Body).Decode(&res) == nil {
			if qr, ok := res["quoteResponse"].(map[string]interface{}); ok {
				if results, ok := qr["result"].([]interface{}); ok && len(results) > 0 {
					if data, ok := results[0].(map[string]interface{}); ok {
						if shares, ok := data["sharesOutstanding"].(float64); ok && shares > 0 {
							resp.Body.Close()
							return shares
						}
					}
				}
			}
		}
		resp.Body.Close()
	} else if resp != nil {
		resp.Body.Close()
	}

	respL, errL := httpGetWithRetry("https://openapi.twse.com.tw/v1/opendata/t187ap03_L")
	if errL == nil {
		defer respL.Body.Close()
		var data []map[string]interface{}
		body, _ := io.ReadAll(respL.Body)
		if json.Unmarshal(body, &data) == nil {
			for _, company := range data {
				id1 := fmt.Sprintf("%v", company["公司代號"])
				id2 := fmt.Sprintf("%v", company["Code"])
				if id1 == stockID || id2 == stockID {
					capStr := strings.ReplaceAll(fmt.Sprintf("%v", company["實收資本額"]), ",", "")
					capital, _ := strconv.ParseFloat(capStr, 64)
					return capital / 10
				}
			}
		}
	}

	respOtc, errOtc := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/mopsfin_t187ap03_O")
	if errOtc == nil {
		defer respOtc.Body.Close()
		var dataOtc []map[string]interface{}
		bodyOtc, _ := io.ReadAll(respOtc.Body)
		if json.Unmarshal(bodyOtc, &dataOtc) == nil {
			for _, company := range dataOtc {
				id1 := fmt.Sprintf("%v", company["公司代號"])
				id2 := fmt.Sprintf("%v", company["SecuritiesCompanyCode"])
				if id1 == stockID || id2 == stockID {
					capStr := strings.ReplaceAll(fmt.Sprintf("%v", company["實收資本額"]), ",", "")
					capital, _ := strconv.ParseFloat(capStr, 64)
					return capital / 10
				}
			}
		}
	}
	
	fileName := "tdcc_cache.csv"
	file, errOpen := os.Open(fileName)
	if errOpen == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		var total float64
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			parts := strings.Split(line, ",")
			if len(parts) >= 6 {
				id := strings.TrimSpace(strings.ReplaceAll(parts[1], "\"", ""))
				id = strings.ReplaceAll(id, "=", "")
				if id == stockID {
					sharesStr := strings.TrimSpace(strings.ReplaceAll(parts[4], "\"", ""))
					shares, _ := strconv.ParseFloat(sharesStr, 64)
					level, _ := strconv.Atoi(strings.TrimSpace(strings.ReplaceAll(parts[2], "\"", "")))
					
					if level >= 1 && level <= 15 {
						total += shares
					}
				}
			}
		}
		if total > 0 { return total }
	}

	return 0
}

func safeDivide(n, d float64) float64 {
	if d == 0 { return 0 }
	return (n / d) * 100
}

func fetchAdvancedMarginData(stockID, currentDate string) (MarginDetail, error) {
	var finalDetail MarginDetail
	var finalErr error = fmt.Errorf("查無資料")
	var found bool
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		date := currentDate
		for i := 0; i < 3; i++ {
			mu.Lock()
			isFound := found
			mu.Unlock()
			if isFound { return }

			url := fmt.Sprintf("https://www.twse.com.tw/rwd/zh/marginTrading/MI_MARGN?date=%s&selectType=ALL&response=csv", date)
			resp, err := httpGetWithRetry(url)
			if err == nil {
				utf8Reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
				utf8Data, _ := io.ReadAll(utf8Reader)
				resp.Body.Close()
				lines := strings.Split(string(utf8Data), "\n")
				reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
				reader.LazyQuotes = true
				reader.FieldsPerRecord = -1
				
				if records, err := reader.ReadAll(); err == nil {
					for _, fields := range records {
						if len(fields) > 13 {
							id := strings.ReplaceAll(fields[0], "\"", "")
							id = strings.ReplaceAll(id, "=", "")
							id = strings.TrimSpace(id)
							
							if id == stockID {
								var detail MarginDetail
								detail.TrendEvals = []string{}
								parseF := func(s string) float64 { v, _ := parseFinancialNumber(s); return v }
								
								detail.FinBuy = parseF(fields[2])
								detail.FinSell = parseF(fields[3])
								detail.FinCashRepay = parseF(fields[4])
								detail.FinPrevBalance = parseF(fields[5])
								detail.FinCurrentBalance = parseF(fields[6])
								detail.FinQuota = parseF(fields[7])
								
								detail.SecBuy = parseF(fields[8])
								detail.SecSell = parseF(fields[9])
								detail.SecStockRepay = parseF(fields[10])
								detail.SecPrevBalance = parseF(fields[11])
								detail.SecCurrentBalance = parseF(fields[12])
								detail.SecQuota = parseF(fields[13])

								detail.FinUsage = safeDivide(detail.FinCurrentBalance, detail.FinQuota)
								detail.SecUsage = safeDivide(detail.SecCurrentBalance, detail.SecQuota)
								detail.FinChangeRate = safeDivide(detail.FinCurrentBalance-detail.FinPrevBalance, detail.FinPrevBalance)
								detail.SecChangeRate = safeDivide(detail.SecCurrentBalance-detail.SecPrevBalance, detail.SecPrevBalance)

								if detail.FinCurrentBalance > 0 { 
									detail.MarginShortRatio = (detail.SecCurrentBalance / detail.FinCurrentBalance) * 100 
								} else {
									detail.MarginShortRatio = 0
								}
								
								detail.SqueezeForce = detail.FinChangeRate - detail.SecChangeRate

								detail.FiveDayAvgShort = detail.SecPrevBalance
								if detail.FiveDayAvgShort > 0 {
									detail.ShortSqueezeStr = safeDivide(detail.FiveDayAvgShort-detail.SecCurrentBalance, detail.FiveDayAvgShort)
								} else {
									detail.ShortSqueezeStr = 0
								}

								if detail.SecCurrentBalance == 0 {
									detail.TrendEvals = append(detail.TrendEvals, "⚠️ 融券餘額為 0，市場無做空籌碼，券資比極低")
								} else if detail.SecUsage > 80 { 
									detail.TrendEvals = append(detail.TrendEvals, "⚠️ 融券使用率 > 80%，看空情緒極端濃厚，隨時可能『軋空』") 
								} else if detail.SecUsage > 50 { 
									detail.TrendEvals = append(detail.TrendEvals, "📉 融券使用率 50~80%，市場偏空") 
								} else { 
									detail.TrendEvals = append(detail.TrendEvals, "🔄 融券使用率 < 50%，市場看空力道不強") 
								}
								
								if detail.SqueezeForce > 10 {
									detail.SqueezeEval = "🚀 >10，市場多方動能強，可能觸發軋空行情！"
								} else if detail.SqueezeForce > 5 {
									detail.SqueezeEval = "📈 5~10，市場多方動能增強，股價可能上漲"
								} else if int(detail.SqueezeForce) > -5 {
									detail.SqueezeEval = "🔄 -5~5，多空力量均衡，市場相對穩定"
								} else if int(detail.SqueezeForce) > -10 {
									detail.SqueezeEval = "📉 -10~-5，空方力量增強，股價可能承壓"
								} else {
									detail.SqueezeEval = "⚠️ <-10，市場空方動能強，可能發生多殺多風險！"
								}
								
								if detail.ShortSqueezeStr >= 100 {
									detail.ShortSqueezeEval = "🚀 100% 空單全數回補，軋空完畢！"
								} else if detail.ShortSqueezeStr > 20 {
									detail.ShortSqueezeEval = "🚀 > 20，有明顯軋空行情，股價可能快速拉升！"
								} else if detail.ShortSqueezeStr > 10 {
									detail.ShortSqueezeEval = "📈 > 10，可能有部分軋空力道"
								} else {
									detail.ShortSqueezeEval = "🔄 空單回補不明顯"
								}

								mu.Lock()
								if !found {
									finalDetail = detail
									finalErr = nil
									found = true
								}
								mu.Unlock()
								return
							}
						}
					}
				}
			}
			date = previousDate(date)
		}
	}()

	go func() {
		defer wg.Done()
		date := currentDate
		for i := 0; i < 3; i++ {
			mu.Lock()
			isFound := found
			mu.Unlock()
			if isFound { return }

			y, _ := strconv.Atoi(date[0:4])
			rocDate := fmt.Sprintf("%d/%s/%s", y-1911, date[4:6], date[6:8])
			urlTPEx := fmt.Sprintf("https://www.tpex.org.tw/web/stock/margin_trading/margin_balance/margin_bal_result.php?l=zh-tw&o=json&d=%s", rocDate)
			respTPEx, errTPEx := httpGetWithRetry(urlTPEx)
			if errTPEx == nil {
				var result map[string]interface{}
				if errJson := json.NewDecoder(respTPEx.Body).Decode(&result); errJson == nil {
					if targetData := extractTPExData(result); targetData != nil {
						for _, item := range targetData {
							row := item.([]interface{})
							if len(row) >= 14 && fmt.Sprintf("%v", row[0]) == stockID {
								var detail MarginDetail
								detail.TrendEvals = []string{}
								parseF := func(s interface{}) float64 { v, _ := parseFinancialNumber(fmt.Sprintf("%v", s)); return v }
								
								if len(row) >= 17 {
									detail.FinPrevBalance = parseF(row[2])
									detail.FinBuy = parseF(row[3])
									detail.FinSell = parseF(row[4])
									detail.FinCashRepay = parseF(row[5])
									detail.FinCurrentBalance = parseF(row[6])
									detail.FinQuota = parseF(row[8])

									detail.SecPrevBalance = parseF(row[10])
									detail.SecBuy = parseF(row[11])
									detail.SecSell = parseF(row[12])
									detail.SecStockRepay = parseF(row[13])
									detail.SecCurrentBalance = parseF(row[14])
									detail.SecQuota = parseF(row[16])
								} else {
									detail.FinPrevBalance = parseF(row[2])
									detail.FinBuy = parseF(row[3])
									detail.FinSell = parseF(row[4])
									detail.FinCashRepay = parseF(row[5])
									detail.FinCurrentBalance = parseF(row[6])
									detail.FinQuota = parseF(row[7])

									detail.SecPrevBalance = parseF(row[8])
									detail.SecBuy = parseF(row[9])
									detail.SecSell = parseF(row[10])
									detail.SecStockRepay = parseF(row[11])
									detail.SecCurrentBalance = parseF(row[12])
									detail.SecQuota = parseF(row[13])
								}

								detail.FinUsage = safeDivide(detail.FinCurrentBalance, detail.FinQuota)
								detail.SecUsage = safeDivide(detail.SecCurrentBalance, detail.SecQuota)
								detail.FinChangeRate = safeDivide(detail.FinCurrentBalance-detail.FinPrevBalance, detail.FinPrevBalance)
								detail.SecChangeRate = safeDivide(detail.SecCurrentBalance-detail.SecPrevBalance, detail.SecPrevBalance)

								if detail.FinCurrentBalance > 0 { 
									detail.MarginShortRatio = (detail.SecCurrentBalance / detail.FinCurrentBalance) * 100 
								} else {
									detail.MarginShortRatio = 0
								}
								
								detail.SqueezeForce = detail.FinChangeRate - detail.SecChangeRate

								detail.FiveDayAvgShort = detail.SecPrevBalance
								if detail.FiveDayAvgShort > 0 {
									detail.ShortSqueezeStr = safeDivide(detail.FiveDayAvgShort-detail.SecCurrentBalance, detail.FiveDayAvgShort)
								} else {
									detail.ShortSqueezeStr = 0
								}

								if detail.SecCurrentBalance == 0 {
									detail.TrendEvals = append(detail.TrendEvals, "⚠️ 融券餘額為 0，市場無做空籌碼，券資比極低")
								} else if detail.SecUsage > 80 { 
									detail.TrendEvals = append(detail.TrendEvals, "⚠️ 融券使用率 > 80%，看空情緒極端濃厚，隨時可能『軋空』") 
								} else if detail.SecUsage > 50 { 
									detail.TrendEvals = append(detail.TrendEvals, "📉 融券使用率 50~80%，市場偏空") 
								} else { 
									detail.TrendEvals = append(detail.TrendEvals, "🔄 融券使用率 < 50%，市場看空力道不強") 
								}
								
								if detail.SqueezeForce > 10 {
									detail.SqueezeEval = "🚀 >10，市場多方動能強，可能觸發軋空行情！"
								} else if detail.SqueezeForce > 5 {
									detail.SqueezeEval = "📈 5~10，市場多方動能增強，股價可能上漲"
								} else if int(detail.SqueezeForce) > -5 {
									detail.SqueezeEval = "🔄 -5~5，多空力量均衡，市場相對穩定"
								} else if int(detail.SqueezeForce) > -10 {
									detail.SqueezeEval = "📉 -10~-5，空方力量增強，股價可能承壓"
								} else {
									detail.SqueezeEval = "⚠️ <-10，市場空方動能強，可能發生多殺多風險！"
								}
								
								if detail.ShortSqueezeStr >= 100 {
									detail.ShortSqueezeEval = "🚀 100% 空單全數回補，軋空完畢！"
								} else if detail.ShortSqueezeStr > 20 {
									detail.ShortSqueezeEval = "🚀 > 20，有明顯軋空行情，股價可能快速拉升！"
								} else if detail.ShortSqueezeStr > 10 {
									detail.ShortSqueezeEval = "📈 > 10，可能有部分軋空力道"
								} else {
									detail.ShortSqueezeEval = "🔄 空單回補不明顯"
								}

								mu.Lock()
								if !found {
									finalDetail = detail
									finalErr = nil
									found = true
								}
								mu.Unlock()
								return
							}
						}
					}
				}
				respTPEx.Body.Close()
			}
			date = previousDate(date)
		}
	}()

	wg.Wait()
	return finalDetail, finalErr
}

func fetchLargeShareholders(stockID string) (float64, float64, bool) {
	type chipResult struct {
		super float64
		large float64
	}
	resChan := make(chan chipResult, 10)
	var wg sync.WaitGroup

	mirrors := []string{
		"fubon-ebrokerdj.fbs.com.tw",
		"djinfo.cathaysec.com.tw",
		"stock.capital.com.tw",
		"mdjmac.megasec.com.tw",
	}

	for _, m := range mirrors {
		wg.Add(1)
		go func(mirror string) {
			defer wg.Done()
			
			url := fmt.Sprintf("https://%s/z/zc/zcj/zcj_%s.djhtm", mirror, stockID)
			resp, err := httpGetWithRetry(url)
			if err != nil { return }
			defer resp.Body.Close()

			reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
			utf8Data, err := io.ReadAll(reader)
			if err != nil { return }

			html := strings.ToLower(string(utf8Data))
			rows := strings.Split(html, "<tr")
			for _, row := range rows {
				cells := extractCells(row)
				if len(cells) >= 5 && strings.ContainsAny(cells[0], "0123456789") && (strings.Contains(cells[0], "/") || strings.Contains(cells[0], "-")) {
					var percentages []float64
					for i := 3; i < len(cells); i++ {
						if val, err := parseFinancialNumber(strings.ReplaceAll(cells[i], "%", "")); err == nil && val > 0 && val <= 100.01 {
							percentages = append(percentages, val)
						}
					}
					if len(percentages) >= 2 {
						v400 := percentages[0]
						v1000 := percentages[len(percentages)-1]
						if v400 >= v1000 {
							resChan <- chipResult{super: v1000, large: v400}
							return
						}
					}
				}
			}
		}(m)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		url := fmt.Sprintf("https://histock.tw/stock/large.aspx?no=%s", stockID)
		resp, err := httpGetWithRetry(url)
		if err != nil { return }
		defer resp.Body.Close()
		
		bytes, _ := io.ReadAll(resp.Body)
		html := strings.ToLower(string(bytes))
		
		rows := strings.Split(html, "<tr")
		for _, row := range rows {
			cells := extractCells(row)
			if len(cells) >= 5 && strings.ContainsAny(cells[0], "0123456789") {
				var percentages []float64
				for i := 1; i < len(cells); i++ {
					if strings.Contains(cells[i], "%") {
						if val, err := parseFinancialNumber(strings.ReplaceAll(cells[i], "%", "")); err == nil && val > 0 && val <= 100.01 {
							percentages = append(percentages, val)
						}
					}
				}
				if len(percentages) >= 2 {
					v400 := percentages[0]
					v1000 := percentages[1]
					if v400 >= v1000 {
						resChan <- chipResult{super: v1000, large: v400}
						return
					}
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		startDate := time.Now().AddDate(0, -6, 0).Format("2006-01-02")
		url := fmt.Sprintf("https://api.finmindtrade.com/api/v4/data?dataset=TaiwanStockHoldingSharesPer&data_id=%s&start_date=%s", stockID, startDate)
		resp, err := httpGetWithRetry(url)
		if err != nil { return }
		defer resp.Body.Close()
		
		var result struct {
			Data []map[string]interface{} `json:"data"`
		}
		body, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(body, &result) == nil && len(result.Data) > 0 {
			type Hold struct { L, S float64 }
			dm := make(map[string]*Hold)
			
			for _, item := range result.Data {
				dateStr := fmt.Sprintf("%v", item["date"])
				if dm[dateStr] == nil { dm[dateStr] = &Hold{} }
				
				levelStr := strings.ReplaceAll(fmt.Sprintf("%v", item["HoldingSharesLevel"]), ",", "")
				levelStr = strings.ReplaceAll(levelStr, " ", "")
				
				isLarge := strings.Contains(levelStr, "400001") || strings.Contains(levelStr, "600001") || 
				           strings.Contains(levelStr, "800001") || strings.Contains(levelStr, "1000001") ||
				           strings.Contains(levelStr, "1000000") ||
				           levelStr == "12" || levelStr == "13" || levelStr == "14" || levelStr == "15"
				isSuper := strings.Contains(levelStr, "1000001") || strings.Contains(levelStr, "1000000") || levelStr == "15"
				
				percent, _ := strconv.ParseFloat(fmt.Sprintf("%v", item["percent"]), 64)
				if isLarge { dm[dateStr].L += percent }
				if isSuper { dm[dateStr].S += percent }
			}
			
			var dates []string
			for d := range dm { dates = append(dates, d) }
			sort.Sort(sort.Reverse(sort.StringSlice(dates)))
			
			if len(dates) > 0 {
				latest := dates[0]
				if dm[latest].L > 0 && dm[latest].L <= 100.01 {
					resChan <- chipResult{super: dm[latest].S, large: dm[latest].L}
					return
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		close(resChan)
	}()

WaitLoop:
	for {
		select {
		case res, ok := <-resChan:
			if !ok { break WaitLoop }
			if res.large > 0 && res.large <= 100.01 {
				return res.super, res.large, true
			}
		case <-time.After(12 * time.Second):
			break WaitLoop
		}
	}
	
	fileName := "tdcc_cache.csv"
	info, errStat := os.Stat(fileName)
	if errStat == nil && info.Size() >= 10000000 {
		file, errOpen := os.Open(fileName)
		if errOpen == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			buf := make([]byte, 0, 64*1024)
			scanner.Buffer(buf, 1024*1024)
			
			var tdccLargeRatio, tdccSuperRatio float64
			found := false

			scanner.Scan()
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" { continue }
				parts := strings.Split(line, ",")
				if len(parts) < 6 { continue }

				id := strings.ReplaceAll(parts[1], "\"", "")
				id = strings.ReplaceAll(id, "=", "")
				id = strings.TrimSpace(id)

				if id == stockID {
					found = true
					levelStr := strings.ReplaceAll(parts[2], "\"", "")
					levelStr = strings.ReplaceAll(levelStr, "=", "")
					level, _ := strconv.Atoi(strings.TrimSpace(levelStr))

					percentStr := strings.ReplaceAll(parts[5], "\"", "")
					percentStr = strings.ReplaceAll(percentStr, "=", "")
					percentStr = strings.ReplaceAll(percentStr, "%", "")
					percent, _ := parseFinancialNumber(percentStr)

					if level >= 12 && level <= 15 { tdccLargeRatio += percent }
					if level == 15 { tdccSuperRatio += percent }
				}
			}
			if found && tdccLargeRatio > 0 && tdccLargeRatio <= 100.01 { 
				return tdccSuperRatio, tdccLargeRatio, false
			}
		}
	}
	
	return 0, 0, false
}

func simulateStrategy(name string, desc string, dates []string, prices []float64, signals []int) BacktestResult {
	initialCapital := 1000000.0
	capital := initialCapital
	position := 0.0
	maxCapital, minCapital := capital, capital
	winTrades, totalTrades := 0, 0
	buyPrice := 0.0

	var trades []TradeRecord

	for i, signal := range signals {
		price := prices[i]
		date := dates[i]

		if signal == 1 && position == 0 { 
			sharesToBuy := math.Floor((capital * 0.95) / price)
			if sharesToBuy > 0 { 
				position = sharesToBuy 
				capital -= sharesToBuy * price 
				buyPrice = price 
				
				trades = append(trades, TradeRecord{Date: date, Action: "買進", Price: price, Shares: sharesToBuy, Capital: capital, Profit: 0})
			}
		} else if signal == -1 && position > 0 { 
			profit := (price - buyPrice) * position
			capital += position * price
			
			trades = append(trades, TradeRecord{Date: date, Action: "賣出", Price: price, Shares: position, Capital: capital, Profit: profit})
			
			position = 0
			totalTrades++
			if price > buyPrice { winTrades++ }
		}
		
		curr := capital + (position * price)
		if curr > maxCapital { maxCapital = curr }
		if curr < minCapital { minCapital = curr }
	}
	
	final := capital + (position * prices[len(prices)-1])
	ret := ((final - initialCapital) / initialCapital) * 100
	mdd := ((maxCapital - minCapital) / maxCapital) * 100
	wr := 0.0
	if totalTrades > 0 { wr = (float64(winTrades) / float64(totalTrades)) * 100 }
	
	return BacktestResult{
		Strategy:     name,
		Description:  desc, 
		TotalReturn:  math.Round(ret*100)/100, 
		MaxDrawdown:  math.Round(mdd*100)/100, 
		WinRate:      math.Round(wr*100)/100, 
		FinalCapital: math.Round(final),
		Trades:       trades, 
	}
}

func runRSILogic(prices []float64) []int {
	signals := make([]int, len(prices))
	if len(prices) < 14 { return signals }
	for i := 14; i < len(prices); i++ {
		u, d := 0.0, 0.0
		for j := i - 13; j <= i; j++ { if diff := prices[j] - prices[j-1]; diff > 0 { u += diff } else { d -= diff } }
		rs := 100.0
		if d != 0 { rs = (u/14) / (d/14) }
		rsi := 100.0 - (100.0 / (1.0 + rs))
		if rsi < 30 { signals[i] = 1 } else if rsi > 70 { signals[i] = -1 }
	}
	return signals
}

func runSMALogic(prices []float64) []int {
	signals := make([]int, len(prices))
	if len(prices) < 20 { return signals }
	for i := 20; i < len(prices); i++ {
		sum := 0.0
		for j := i - 19; j <= i; j++ { sum += prices[j] }
		sma := sum / 20.0
		if prices[i] > sma && prices[i-1] <= sma { signals[i] = 1 } else if prices[i] < sma && prices[i-1] >= sma { signals[i] = -1 }
	}
	return signals
}

func runMACDLogic(prices []float64) []int {
	signals := make([]int, len(prices))
	if len(prices) < 35 { return signals }
	e12, e26 := prices[0], prices[0]
	var difs []float64
	for i := 0; i < len(prices); i++ {
		e12, e26 = (prices[i]*2+e12*11)/13, (prices[i]*2+e26*25)/27
		difs = append(difs, e12-e26)
	}
	macd := difs[0]
	for i := 0; i < len(prices); i++ {
		macd = (difs[i]*2+macd*8)/10
		if i > 0 {
			pMacd := (difs[i-1]*2+macd*8)/10
			if difs[i] > macd && difs[i-1] <= pMacd { signals[i] = 1 } else if difs[i] < macd && difs[i-1] >= pMacd { signals[i] = -1 }
		}
	}
	return signals
}

func runKDLogic(prices, highs, lows []float64) []int {
	signals := make([]int, len(prices))
	if len(prices) < 9 { return signals }
	k, d := 50.0, 50.0
	for i := 8; i < len(prices); i++ {
		hi, lo := highs[i], lows[i]
		for j := i - 8; j <= i; j++ { if highs[j] > hi { hi = highs[j] }; if lows[j] < lo { lo = lows[j] } }
		rsv := 50.0
		if hi != lo { rsv = 100 * (prices[i] - lo) / (hi - lo) }
		pK, pD := k, d
		k, d = (2.0/3.0)*pK+(1.0/3.0)*rsv, (2.0/3.0)*pD+(1.0/3.0)*k
		if k > d && pK <= pD && k < 30 { signals[i] = 1 } else if k < d && pK >= pD && k > 70 { signals[i] = -1 }
	}
	return signals
}

func runBollingerLogic(prices []float64) []int {
	period := 20
	signals := make([]int, len(prices))
	if len(prices) < period { return signals }
	for i := period - 1; i < len(prices); i++ {
		sum := 0.0
		for j := i - period + 1; j <= i; j++ { sum += prices[j] }
		sma := sum / float64(period)
		sumSquares := 0.0
		for j := i - period + 1; j <= i; j++ {
			diff := prices[j] - sma
			sumSquares += diff * diff
		}
		stdDev := math.Sqrt(sumSquares / float64(period))
		upperBand, lowerBand := sma+2*stdDev, sma-2*stdDev
		if prices[i] < lowerBand { signals[i] = 1 } else if prices[i] > upperBand { signals[i] = -1 }
	}
	return signals
}

func runMomentumLogic(prices []float64) []int {
	period := 10
	signals := make([]int, len(prices))
	if len(prices) < period { return signals }
	for i := period; i < len(prices); i++ {
		momentum := prices[i] - prices[i-period]
		if momentum > 0 { signals[i] = 1 } else if momentum < 0 { signals[i] = -1 }
	}
	return signals
}

// 🎯 runVolumeRatioLogic：依據新規則：均量比 > 1.5 買進 ； 跌破 10MA 或 均量比 > 2.0 且為實體長黑 K 平倉
func runVolumeRatioLogic(prices, opens, volumes []float64) []int {
	period := 10
	signals := make([]int, len(prices))
	if len(prices) < period || len(volumes) < period { return signals }

	// 1. 計算 10 日均線 (10MA 價格)
	ma10 := make([]float64, len(prices))
	for i := period - 1; i < len(prices); i++ {
		sumP := 0.0
		for j := i - period + 1; j <= i; j++ { sumP += prices[j] }
		ma10[i] = sumP / float64(period)
	}

	// 2. 計算 10 日均量比值 (成交量 10MA)
	volRatio := make([]float64, len(volumes))
	for i := period; i < len(volumes); i++ {
		sumV := 0.0
		for j := i - period; j < i; j++ { sumV += volumes[j] }
		avgV := sumV / float64(period)
		if avgV > 0 {
			volRatio[i] = volumes[i] / avgV
		}
	}

	// 3. 判定買賣訊號
	for i := period; i < len(prices); i++ {
		vr := volRatio[i]
		p := prices[i]
		o := opens[i]

		// 判定實體長黑 K (收盤價低於開盤價且實體跌幅 >= 1.5%)
		isBlackK := p < o
		isLongBlackK := isBlackK && (o-p)/o >= 0.015
		if o == 0 && i > 0 { // 開盤價缺失時以昨收比對
			isLongBlackK = p < prices[i-1] && (prices[i-1]-p)/prices[i-1] >= 0.02
		}

		if vr > 1.5 {
			signals[i] = 1 // 均量比值 > 1.5 買進
		} else if (ma10[i] > 0 && p < ma10[i]) || (vr > 2.0 && isLongBlackK) {
			signals[i] = -1 // 跌破 10MA 或 均量比 > 2.0 且為實體長黑 K 平倉
		}
	}
	return signals
}

// =========================================================
// 專屬觀察清單 (Watchlist) 系統
// =========================================================

type WatchlistItem struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	AddDate  string  `json:"addDate"`
	AddPrice float64 `json:"addPrice"`
}

type WatchlistStat struct {
	CurrentPrice     float64 `json:"currentPrice"`
	BasePrice        float64 `json:"basePrice"`
	HighestPrice     float64 `json:"highestPrice"`
	HighestGain      float64 `json:"highestGain"`
	ElapsedDays      int     `json:"elapsedDays"`
	TotalTradingDays int     `json:"totalTradingDays"`
}

func getWatchlistFile() string {
	return "watchlist.json"
}

func (a *App) LoadWatchlist() []WatchlistItem {
	file, err := os.ReadFile(getWatchlistFile())
	if err != nil { return []WatchlistItem{} }
	var list []WatchlistItem
	json.Unmarshal(file, &list)
	return list
}

func (a *App) SaveWatchlist(list []WatchlistItem) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil { return err }
	return os.WriteFile(getWatchlistFile(), data, 0644)
}

func (a *App) GetWatchlistStats(items []WatchlistItem) map[string]WatchlistStat {
	result := make(map[string]WatchlistStat)
	if len(items) == 0 { return result }

	var ids []string
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	
	currentPrices := a.GetCurrentPrices(ids)

	now := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, item := range items {
		wg.Add(1)
		go func(stockItem WatchlistItem) {
			defer wg.Done()

			stat := WatchlistStat{}
			if cp, ok := currentPrices[stockItem.ID]; ok {
				stat.CurrentPrice = cp
			}

			basePrice := stockItem.AddPrice 

			addDate, err := time.Parse("2006-01-02", stockItem.AddDate)
			if err == nil {
				records, errYahoo := downloadYahooFinance(stockItem.ID, addDate.Unix(), now.Unix())
				highestPrice := stockItem.AddPrice
				
				tradingDaysElapsed := 0
				highestTradingDays := 0
				
				if errYahoo == nil && len(records) > 1 {
					for i := 1; i < len(records); i++ {
						row := records[i]
						if len(row) > 4 && row[4] != "null" {
							parsedDate, errDate := time.Parse("2006-01-02", row[0])
							if errDate == nil {
								if parsedDate.Before(addDate) {
									continue
								}

								closePrice, _ := strconv.ParseFloat(row[4], 64)

								if parsedDate.Equal(addDate) {
									basePrice = closePrice
									if highestPrice == stockItem.AddPrice {
										highestPrice = closePrice
									}
								}

								if parsedDate.After(addDate) {
									tradingDaysElapsed++
								}

								if closePrice > highestPrice {
									highestPrice = closePrice
									highestTradingDays = tradingDaysElapsed
								}
							}
						}
					}
				}
				
				stat.BasePrice = basePrice
				stat.HighestPrice = highestPrice
				stat.ElapsedDays = highestTradingDays
				stat.TotalTradingDays = tradingDaysElapsed 

				if basePrice > 0 {
					stat.HighestGain = ((highestPrice - basePrice) / basePrice) * 100
				}
			}

			mu.Lock()
			result[stockItem.ID] = stat
			mu.Unlock()

		}(item)
	}

	wg.Wait()
	return result
}

func (a *App) GetCurrentPrices(ids []string) map[string]float64 {
	result := make(map[string]float64)
	if len(ids) == 0 { return result }

	var twseData []interface{}
	var tpexData []map[string]interface{}

	respTWSE, errTWSE := httpGetWithRetry("https://www.twse.com.tw/exchangeReport/STOCK_DAY_ALL?response=json")
	if errTWSE == nil {
		var res map[string]interface{}
		if json.NewDecoder(respTWSE.Body).Decode(&res) == nil {
			if dataList, ok := res["data"].([]interface{}); ok { twseData = dataList }
		}
		respTWSE.Body.Close()
	}

	respOTC, errOTC := httpGetWithRetry("https://www.tpex.org.tw/openapi/v1/tpex_mainboard_quotes")
	if errOTC == nil {
		bodyOtc, _ := io.ReadAll(respOTC.Body)
		json.Unmarshal(bodyOtc, &tpexData)
		respOTC.Body.Close()
	}

	priceMap := make(map[string]float64)
	for _, item := range twseData {
		row, ok := item.([]interface{})
		if !ok || len(row) < 8 { continue }
		
		id := strings.TrimSpace(fmt.Sprintf("%v", row[0]))
		p, _ := strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", row[7]), ",", ""), 64)
		priceMap[id] = p
	}
	
	for _, item := range tpexData {
		id := strings.TrimSpace(fmt.Sprintf("%v", item["SecuritiesCompanyCode"]))
		p, _ := strconv.ParseFloat(strings.ReplaceAll(fmt.Sprintf("%v", item["Close"]), ",", ""), 64)
		priceMap[id] = p
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	
	for _, id := range ids {
		if p, ok := priceMap[id]; ok && p > 0 {
			result[id] = p
		} else {
			wg.Add(1)
			go func(stockID string) {
				defer wg.Done()
				p, _, _, _ := getYahooQuote(stockID) 
				if p > 0 {
					mu.Lock()
					result[stockID] = p
					mu.Unlock()
				}
			}(id)
		}
	}
	wg.Wait()

	return result
}

func getRecent4Quarters(now time.Time) string {
	y := now.Year()
	m := now.Month()
	d := now.Day()

	var latestY, latestQ int

	if m < 3 || (m == 3 && d <= 31) {
		latestY = y - 1
		latestQ = 3
	} else if m < 5 || (m == 5 && d <= 15) {
		latestY = y - 1
		latestQ = 4
	} else if m < 8 || (m == 8 && d <= 14) {
		latestY = y
		latestQ = 1
	} else if m < 11 || (m == 11 && d <= 14) {
		latestY = y
		latestQ = 2
	} else {
		latestY = y
		latestQ = 3
	}

	var quarters []string
	currY, currQ := latestY, latestQ
	for i := 0; i < 4; i++ {
		quarters = append(quarters, fmt.Sprintf("%dQ%d", currY, currQ))
		currQ--
		if currQ == 0 {
			currY--
			currQ = 4
		}
	}

	return strings.Join(quarters, "、")
}