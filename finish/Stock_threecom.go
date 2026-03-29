/*
台股個股籌碼與多空趨勢分析爬蟲
功能：
1. 取得三大法人買賣超資訊 (TWSE)
2. 計算個股當沖比 (當沖量 / 總成交量)
3. 取得散戶動向 (融資融券增減)
4. 取得大戶與超級大戶持股比例 (FinMind API / TDCC 集保所)
5. 自動計算近四週大戶持股變化 (若 API 阻擋則自動切換官方集保所最新資料)
6. 綜合輸出多空判斷指標
*/
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// ============================================================================
// 日期處理函數區塊
// ============================================================================

// getLatestTradingDate 取得最近的一個「可能」的交易日 (排除週六、週日)
func getLatestTradingDate() string {
	now := time.Now()
	weekday := now.Weekday()
	if weekday == time.Sunday {
		now = now.AddDate(0, 0, -2) // 週日往前推兩天到週五
	} else if weekday == time.Saturday {
		now = now.AddDate(0, 0, -1) // 週六往前推一天到週五
	}
	return now.Format("20060102") // 格式化為 YYYYMMDD
}

// findValidTradingDate 往前尋找最近 10 天內真正有開盤(有資料)的日期
func findValidTradingDate() string {
	date := getLatestTradingDate()
	for i := 0; i < 10; i++ {
		fmt.Printf("🔍 嘗試查詢日期: %s\n", date)
		if hasData(date) {
			return date
		}
		date = previousDate(date) // 若無資料則往前推一天
	}
	fmt.Println("⚠️ 無法找到最近 10 天內的有效交易資料")
	return getLatestTradingDate()
}

// hasData 發送簡單請求至證交所，檢查該日期是否有交易資料 (避免遇到國定假日)
func hasData(date string) bool {
	url := fmt.Sprintf("https://www.twse.com.tw/rwd/zh/fund/T86?date=%s&selectType=ALL&response=csv", date)
	client := &http.Client{}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	
	// 只讀取前幾行，若出現 html 標籤代表查無資料 (證交所找不到資料會回傳 HTML 錯誤頁)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "<!DOCTYPE html>") {
			return false
		}
		if strings.Contains(line, ",") { // 只要有逗號代表是正常的 CSV 資料
			return true
		}
	}
	return false
}

// previousDate 計算上一個交易日 (跨越週末)
func previousDate(date string) string {
	t, _ := time.Parse("20060102", date)
	t = t.AddDate(0, 0, -1)
	if t.Weekday() == time.Saturday {
		t = t.AddDate(0, 0, -1)
	}
	if t.Weekday() == time.Sunday {
		t = t.AddDate(0, 0, -2)
	}
	return t.Format("20060102")
}

// ============================================================================
// 三大法人買賣超 (T86) 處理區塊
// ============================================================================

// fetchCSV 從證交所下載當日所有股票的三大法人買賣超 CSV 檔案，並將 Big5 轉為 UTF-8
func fetchCSV(date string) ([]string, error) {
	url := fmt.Sprintf("https://www.twse.com.tw/rwd/zh/fund/T86?date=%s&selectType=ALL&response=csv", date)
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("建立 HTTP 請求失敗: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下載 CSV 失敗: %v", err)
	}
	defer resp.Body.Close()

	// 證交所的 CSV 是 Big5 編碼，必須使用 transform 轉為 UTF-8 否則會亂碼
	utf8Reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
	utf8Data, err := ioutil.ReadAll(utf8Reader)
	if err != nil {
		return nil, fmt.Errorf("轉換 Big5 為 UTF-8 失敗: %v", err)
	}
	
	lines := strings.Split(string(utf8Data), "\n")
	if len(lines) == 0 || strings.Contains(lines[0], "<!DOCTYPE html>") {
		return nil, fmt.Errorf("⚠️ 當日 (%s) 無法人交易資料", date)
	}
	return lines, nil
}

// getValidLotsStr 工具函數：清除數字字串中的逗號，並將「股數」除以 1000 轉換為「張數」字串
func getValidLotsStr(record []string, index int) string {
	if index < len(record) {
		raw := strings.ReplaceAll(strings.TrimSpace(record[index]), ",", "")
		if raw == "" {
			return "0"
		}
		val, err := strconv.ParseFloat(raw, 64)
		if err == nil {
			return fmt.Sprintf("%.0f", val/1000) // 除以 1000 轉為張
		}
	}
	return "0"
}

// parseCSV 解析三大法人 CSV，尋找使用者輸入的特定股票代號，並萃取其買賣超張數
func parseCSV(lines []string, stockID string, date string) (map[string]string, error) {
	csvData := strings.Join(lines, "\n")
	reader := csv.NewReader(strings.NewReader(csvData))
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析 CSV 失敗: %v", err)
	}
	
	// 逐行尋找相符的股票代號
	for _, record := range records {
		if len(record) > 1 {
			csvStockID := strings.TrimSpace(record[0])
			if csvStockID == stockID {
				data := make(map[string]string)
				data["證券代號"] = csvStockID
				data["證券名稱"] = record[1]
				
				// 取出各個法人的買賣超欄位 (轉為張數)
				data["外陸資買賣超張數"] = getValidLotsStr(record, 4)
				data["投信買賣超張數"] = getValidLotsStr(record, 10)
				data["自營商買賣超張數"] = getValidLotsStr(record, 14)
				data["外資(避險)自營商買賣超張數"] = getValidLotsStr(record, 17)
				data["三大法人買賣超張數"] = getValidLotsStr(record, 18)
				return data, nil
			}
		}
	}
	return nil, fmt.Errorf("⚠️ 股票代號 %s 在當日 (%s) 無交易資料", stockID, date)
}

// ============================================================================
// 個股當沖與總量數據處理區塊
// ============================================================================

// fetchDayTradingForStock 從證交所 (TWTB4U) 取得個股當日「當沖總量 (股)」
func fetchDayTradingForStock(stockID string, date string) (float64, error) {
	url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/TWTB4U?response=csv&date=%s", date)
	resp, err := http.Get(url)
	if err != nil {
		return 0, fmt.Errorf("無法下載 TWTB4U 資料: %v", err)
	}
	defer resp.Body.Close()
	
	utf8Reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
	utf8Data, err := ioutil.ReadAll(utf8Reader)
	if err != nil {
		return 0, fmt.Errorf("轉碼失敗: %v", err)
	}
	
	lines := strings.Split(string(utf8Data), "\n")
	reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("⚠️ CSV 解析失敗: %v", err)
	}
	
	for _, fields := range records {
		if len(fields) < 4 {
			continue
		}
		id := strings.TrimSpace(strings.ReplaceAll(fields[0], "\"", ""))
		if id == stockID {
			// 欄位 3 為當沖成交股數
			raw := strings.ReplaceAll(fields[3], "\"", "")
			raw = strings.ReplaceAll(raw, ",", "")
			raw = strings.ReplaceAll(raw, " ", "")
			vol, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return 0, fmt.Errorf("當沖股數轉換失敗")
			}
			return vol, nil
		}
	}
	return 0, fmt.Errorf("找不到當日成交股數")
}

// fetchDailyTotalVolume 從證交所 (STOCK_DAY) 取得個股當日「總成交量 (股)」
func fetchDailyTotalVolume(date string, stockID string) (float64, error) {
	// API 需要傳入 YYYYMM01 的月份格式
	queryMonth := date[:6] + "01"
	url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/STOCK_DAY?response=csv&date=%s&stockNo=%s", queryMonth, stockID)
	client := &http.Client{}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("下載失敗: %v", err)
	}
	defer resp.Body.Close()
	
	reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
	utf8Data, _ := ioutil.ReadAll(reader)
	scanner := bufio.NewScanner(strings.NewReader(string(utf8Data)))
	
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "/") || !strings.Contains(line, ",") {
			continue // 跳過非資料行
		}
		r := csv.NewReader(strings.NewReader(line))
		r.LazyQuotes = true
		r.FieldsPerRecord = -1
		records, err := r.Read()
		if err != nil || len(records) < 2 {
			continue
		}
		
		// 轉換民國年為西元年 (例如 113/05/20 -> 20240520)
		twDate := strings.TrimSpace(records[0])
		twParts := strings.Split(twDate, "/")
		if len(twParts) != 3 {
			continue
		}
		yy, _ := strconv.Atoi(twParts[0])
		mm, _ := strconv.Atoi(twParts[1])
		dd, _ := strconv.Atoi(twParts[2])
		parsedDate := fmt.Sprintf("%04d%02d%02d", yy+1911, mm, dd)

		rawVol := strings.ReplaceAll(records[1], ",", "")
		rawVol = strings.ReplaceAll(rawVol, "\"", "")
		vol, err := strconv.ParseFloat(strings.TrimSpace(rawVol), 64)
		
		if parsedDate == date {
			return vol, nil // 找到當日總成交量
		}
	}
	return 0, fmt.Errorf("找不到該日成交股數")
}

// fetchMarginTrading 從證交所 (MI_MARGN) 取得當日融資融券餘額，計算出融資買賣超(散戶動向)
func fetchMarginTrading(date string, stockID string) (float64, float64, error) {
	url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/MI_MARGN?response=csv&date=%s&selectType=ALL", date)
	resp, err := http.Get(url)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	utf8Reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
	utf8Data, _ := ioutil.ReadAll(utf8Reader)
	lines := strings.Split(string(utf8Data), "\n")

	reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	records, _ := reader.ReadAll()

	for _, fields := range records {
		if len(fields) > 10 {
			id := strings.TrimSpace(strings.ReplaceAll(fields[0], "\"", ""))
			if id == stockID {
				// 欄位 2: 融資買進, 欄位 3: 融資賣出
				marginBuyStr := strings.ReplaceAll(fields[2], ",", "")
				marginSellStr := strings.ReplaceAll(fields[3], ",", "")
				marginBuy, _ := strconv.ParseFloat(marginBuyStr, 64)
				marginSell, _ := strconv.ParseFloat(marginSellStr, 64)
				
				marginNet := marginBuy - marginSell // 淨融資買賣超
				shortNet := 0.0 // (如需融券可自行擴充)
				return marginNet, shortNet, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("找不到融資資料")
}

// fetchTotalShares 透過證交所 OpenAPI 取得公司「實收資本額」，推算出「總發行股數」用以計算換手率
func fetchTotalShares(stockID string) (float64, error) {
	url := "https://openapi.twse.com.tw/v1/opendata/t187ap03_L"
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var data []map[string]interface{}
	body, _ := ioutil.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}

	for _, company := range data {
		if company["公司代號"] == stockID {
			capitalStr := company["實收資本額"].(string)
			capital, _ := strconv.ParseFloat(capitalStr, 64)
			// 台灣股票面額通常為 10 元，故總股數 = 資本額 / 10
			totalShares := capital / 10
			return totalShares, nil
		}
	}
	return 0, fmt.Errorf("找不到公司資本額資料")
}

// ============================================================================
// 大戶籌碼與集保資料處理區塊 (雙重保險機制)
// ============================================================================

// fetchLargeShareholders 取得大戶籌碼比例與變化
// 回傳值：主力買超(固定0), 超級大戶(1000張)比例, 大戶(400張)比例, 4週變化, 是否有變化資料, 錯誤
func fetchLargeShareholders(stockID string) (float64, float64, float64, float64, bool, error) {
	var principalNet float64 = 0 // 無法輕易抓取主力資料，故固定回傳 0

	// ==================== 第一層: 嘗試 FinMind API 獲取近 4 週歷史資料 ====================
	// 往回推 45 天，確保涵蓋過去 4 到 5 週的每週五結算日
	startDate := time.Now().AddDate(0, 0, -45).Format("2006-01-02")
	url := fmt.Sprintf("https://api.finmindtrade.com/api/v4/data?dataset=TaiwanStockHoldingSharesPer&data_id=%s&start_date=%s", stockID, startDate)

	// 設定 15 秒 Timeout 避免 API 卡住
	apiClient := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	// 偽裝 User-Agent 避免遭到防火牆阻擋
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	
	resp, err := apiClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		body, _ := ioutil.ReadAll(resp.Body)

		var result struct {
			Status int    `json:"status"`
			Msg    string `json:"msg"`
			Data   []struct {
				Date               string  `json:"date"`
				HoldingSharesLevel string  `json:"HoldingSharesLevel"`
				Percent            float64 `json:"percent"`
			} `json:"data"`
		}

		if json.Unmarshal(body, &result) == nil && result.Status == 200 && len(result.Data) > 0 {
			type HoldData struct {
				LargeRatio float64 // 400張以上比例加總
				SuperRatio float64 // 1000張以上比例加總
			}
			dateMap := make(map[string]*HoldData)

			for _, item := range result.Data {
				if dateMap[item.Date] == nil {
					dateMap[item.Date] = &HoldData{}
				}
				
				// 處理欄位字串 (例如 "400,001-600,000")，去除逗號與空白，提取首組數字
				levelStr := strings.ReplaceAll(item.HoldingSharesLevel, ",", "")
				levelStr = strings.ReplaceAll(levelStr, " ", "")
				firstNumStr := ""
				for _, r := range levelStr {
					if r >= '0' && r <= '9' {
						firstNumStr += string(r)
					} else if len(firstNumStr) > 0 {
						break // 遇到連字號就停止
					}
				}

				if firstNumStr != "" {
					val, _ := strconv.Atoi(firstNumStr)
					isLarge := false
					isSuperLarge := false

					// 級距代碼 12~15 代表 400 張以上 (若為數值則是 >= 40萬股)
					if val >= 12 && val <= 15 {
						isLarge = true
						if val == 15 {
							isSuperLarge = true
						}
					} else if val >= 400000 { 
						isLarge = true
						if val >= 1000000 {
							isSuperLarge = true
						}
					}

					// 累加特定日期的比例
					if isLarge {
						dateMap[item.Date].LargeRatio += item.Percent
					}
					if isSuperLarge {
						dateMap[item.Date].SuperRatio += item.Percent
					}
				}
			}

			// 將日期收集並從新到舊排序
			var dates []string
			for d := range dateMap {
				dates = append(dates, d)
			}
			sort.Sort(sort.Reverse(sort.StringSlice(dates)))

			if len(dates) > 0 {
				latestDate := dates[0]
				latestLarge := dateMap[latestDate].LargeRatio
				latestSuper := dateMap[latestDate].SuperRatio

				// 尋找 4 週前的資料 (Index 4 代表第 5 筆)，若資料不足則取最舊的
				pastIdx := 4
				if len(dates) <= 4 {
					pastIdx = len(dates) - 1
				}
				pastDate := dates[pastIdx]
				pastLarge := dateMap[pastDate].LargeRatio

				diff := latestLarge - pastLarge
				// 成功抓取歷史資料，回傳 true
				return principalNet, latestSuper, latestLarge, diff, true, nil
			}
		}
	}

	// ==================== 第二層: 退場防護 (FinMind 失效時改抓 TDCC 官方單週資料) ====================
	// 官方檔案極大 (約 40MB)，需給予 60 秒充裕時間
	tdccClient := &http.Client{Timeout: 60 * time.Second}
	tdccUrl := "https://smart.tdcc.com.tw/opendata/getOD.ashx?id=1-5"
	tdccReq, _ := http.NewRequest("GET", tdccUrl, nil)
	tdccReq.Header.Set("User-Agent", "Mozilla/5.0")
	tdccResp, tdccErr := tdccClient.Do(tdccReq)
	
	if tdccErr != nil {
		return principalNet, 0, 0, 0, false, fmt.Errorf("API與集保所皆無法連線: %v", tdccErr)
	}
	defer tdccResp.Body.Close()

	// 使用 reader 逐行讀取，避免記憶體爆表
	reader := csv.NewReader(tdccResp.Body)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	
	var tdccLargeRatio float64
	var tdccSuperRatio float64
	found := false

	// 跳過標題行
	_, _ = reader.Read()

	for {
		row, err := reader.Read()
		if err != nil {
			break // 讀到檔尾
		}
		if len(row) < 6 {
			continue
		}

		id := strings.TrimSpace(row[1])

		// 🎯 效能優化 (Early Exit): 檔案是依股票代號排序的，若已找到該股，接著又碰到不同代號，代表已讀完，立刻中斷！
		if found && id != stockID {
			break 
		}

		if id == stockID {
			found = true
			level, _ := strconv.Atoi(strings.TrimSpace(row[2]))
			percent, _ := strconv.ParseFloat(strings.TrimSpace(row[5]), 64)

			if level >= 12 && level <= 15 {
				tdccLargeRatio += percent
			}
			if level == 15 {
				tdccSuperRatio += percent
			}
		}
	}

	if !found {
		return principalNet, 0, 0, 0, false, fmt.Errorf("官方檔案中無該股票資料")
	}

	// 僅能取得當週最新資料，無歷史變動，回傳 false
	return principalNet, tdccSuperRatio, tdccLargeRatio, 0.0, false, fmt.Errorf("已啟用防護：自動切換為集保所官方最新資料 (原因: 無法取得歷史趨勢)")
}

// ============================================================================
// 分析與輸出區塊
// ============================================================================

// printStockData 簡單列印法人買賣超資料
func printStockData(data map[string]string) {
	fmt.Println("\n✅ 查詢結果:")
	fmt.Printf("📌 證券代號: %s\n", data["證券代號"])
	fmt.Printf("🏢 證券名稱: %s\n", data["證券名稱"])
	fmt.Printf("💰 外陸資買賣超: %s 張\n", data["外陸資買賣超張數"])
	fmt.Printf("📊 投信買賣超: %s 張\n", data["投信買賣超張數"])
	fmt.Printf("🏦 自營商買賣超: %s 張\n", data["自營商買賣超張數"])
	fmt.Printf("🌍 外資(避險)買賣超: %s 張\n", data["外資(避險)自營商買賣超張數"])
	fmt.Printf("📈 三大法人買賣超: %s 張\n", data["三大法人買賣超張數"])
}

// ============================================================================
// 分析與輸出區塊
// ============================================================================

// analyzeMarketTrend 綜合所有數據進行多空趨勢判斷
func analyzeMarketTrend(
	dayTradeRatio float64,
	foreignNet int,
	investmentNet int,
	dealerNet int,
	hedgeDealerNet int,
	totalInstitutionNet int,
	marginNetLots float64,       
	turnoverRate float64,    
	concentration float64,   
	largeHolderRatio float64,
	largeHolderDiff float64,
	hasDiff bool, // 控制是否顯示四週變化
) {
	fmt.Println("\n📉 多空趨勢與籌碼綜合分析:")

	// 法人動向分析
	if totalInstitutionNet < -3000 {
		fmt.Printf("📊 法人動向: %d 張 → ⚠️ 偏空趨勢 (三大法人同步賣超)\n", totalInstitutionNet)
	} else if totalInstitutionNet > 0 {
		fmt.Printf("📊 法人動向: %d 張 → ✅ 偏多動能\n", totalInstitutionNet)
	} else {
		fmt.Printf("📊 法人動向: %d 張 → ⚠️ 觀望\n", totalInstitutionNet)
	}

	// 散戶融資動向分析
	if marginNetLots > 0 {
		fmt.Printf("🧑‍🤝‍🧑 散戶動向 (融資): +%.0f 張 → ⚠️ 散戶進場，籌碼可能轉亂 (融資餘額增加)\n", marginNetLots)
	} else {
		fmt.Printf("🧑‍🤝‍🧑 散戶動向 (融資): %.0f 張 → ✅ 散戶退場，籌碼相對安定\n", marginNetLots)
	}

	// 當沖比分析 (🎯 新增：處理無當沖資料的狀況)
	if dayTradeRatio > 30 {
		fmt.Printf("💹 當沖比: %.2f%% → ⚠️ 短線客多，股價易大幅波動\n", dayTradeRatio)
	} else if dayTradeRatio > 0 {
		fmt.Printf("💹 當沖比: %.2f%% → ✅ 籌碼相對穩定\n", dayTradeRatio)
	} else {
		fmt.Printf("💹 當沖比: 無當沖資料\n")
	}

	// 換手率分析
	if turnoverRate > 10 {
		fmt.Printf("🔄 換手率: %.2f%% → 🔥 交易爆量熱絡 (可能在做頭或底部爆量換手)\n", turnoverRate)
	} else if turnoverRate > 0 {
		fmt.Printf("🔄 換手率: %.2f%% → 正常量能\n", turnoverRate)
	}

	// 大戶持股比例 (400張以上)
	fmt.Printf("👑 大戶動向 (400張以上): 持股比例 %.2f%%\n", largeHolderRatio)
	
	// 若成功取得歷史數據，才顯示近四週變化；否則靜默隱藏
	if hasDiff {
		if largeHolderDiff > 0 {
			fmt.Printf("📈 近四週大戶變化: +%.2f%% → ✅ 大戶持續進場\n", largeHolderDiff)
		} else if largeHolderDiff < 0 {
			fmt.Printf("📉 近四週大戶變化: %.2f%% → ⚠️ 大戶減碼退場\n", largeHolderDiff)
		} else {
			fmt.Printf("➖ 近四週大戶變化: 0.00%% → 觀望\n")
		}
	}

	// 籌碼集中度 (1000張以上)
	if concentration > 40 {
		fmt.Printf("🎯 籌碼集中度 (1000張以上): %.2f%% → ✅ 籌碼高度集中於超級大戶手中\n", concentration)
	} else if concentration > 20 {
		fmt.Printf("🎯 籌碼集中度 (1000張以上): %.2f%% → 穩定，多數籌碼在大戶手中\n", concentration)
	} else {
		fmt.Printf("🎯 籌碼集中度 (1000張以上): %.2f%% → ⚠️ 籌碼較為分散\n", concentration)
	}
}

// ============================================================================
// 主程式入口
// ============================================================================
func main() {
	date := findValidTradingDate()
	fmt.Printf("📅 使用最近有效交易日: %s\n", date)

	var stockID string
	fmt.Print("請輸入股票代號: ")
	fmt.Scanln(&stockID)

	// 1. 下載並解析法人買賣超 CSV
	lines, err := fetchCSV(date)
	if err != nil {
		fmt.Println("❌ 錯誤:", err)
		return
	}

	stockData, err := parseCSV(lines, stockID, date)
	if err != nil {
		fmt.Println("⚠️", err)
		return
	}
	printStockData(stockData)

	// 2. 獲取成交總量、當沖量與總發行量，計算當沖比與換手率
	totalVol, err := fetchDailyTotalVolume(date, stockID)
	dayTradeVol, err := fetchDayTradingForStock(stockID, date)
	totalShares, _ := fetchTotalShares(stockID)

	var ratio, turnoverRate float64
	fmt.Println("\n==== 個股交易數據 ====")
	if totalVol > 0 {
		if dayTradeVol > 0 {
			ratio = (dayTradeVol / totalVol) * 100
			fmt.Printf("🔁 當日沖銷成交: %.0f 張\n", dayTradeVol/1000)
			fmt.Printf("💹 當沖比: %.2f%%\n", ratio)
			stockData["當沖比"] = fmt.Sprintf("%.2f%%", ratio)
		}
		
		if totalShares > 0 {
			turnoverRate = (totalVol / totalShares) * 100
			fmt.Printf("📊 總成交量: %.0f 張\n", totalVol/1000)
			fmt.Printf("🏦 預估總發行量: %.0f 張\n", totalShares/1000)
			fmt.Printf("🔄 換手率: %.2f%%\n", turnoverRate)
		}
	} else {
		fmt.Println("⚠️ 資料不足，無法計算當沖比與換手率")
	}
	
	// 3. 獲取融資融券餘額 (散戶動向)
	marginNet, _, err := fetchMarginTrading(date, stockID)
	if err != nil {
		fmt.Println("⚠️ 無法取得融資融券資料")
	}
	marginNetLots := marginNet / 1000

	// 4. 取得大戶與超級大戶籌碼比例 (包含雙重防護機制)
	_, concentration, largeHolderRatio, largeHolderDiff, hasDiff, err := fetchLargeShareholders(stockID)
	if err != nil {
		// 提示系統是否切換了退場防護
		fmt.Printf("\n💡 [系統提示]: %v\n", err)
	}

	// 5. 將所有指標整合，進行終端多空分析輸出
	// 🎯 修正：移除對 stockData["當沖比"] 的嚴格條件限制，強制往下執行！
	foreign, _ := strconv.Atoi(stockData["外陸資買賣超張數"])
	investment, _ := strconv.Atoi(stockData["投信買賣超張數"])
	dealer, _ := strconv.Atoi(stockData["自營商買賣超張數"])
	hedgeDealer, _ := strconv.Atoi(stockData["外資(避險)自營商買賣超張數"])
	totalInstitution, _ := strconv.Atoi(stockData["三大法人買賣超張數"])

	analyzeMarketTrend(
		ratio, // 若前面沒算到當沖比，這裡預設傳入就是 0
		foreign,
		investment,
		dealer,
		hedgeDealer,
		totalInstitution,
		marginNetLots,
		turnoverRate,
		concentration,
		largeHolderRatio,
		largeHolderDiff, 
		hasDiff, 
	)
}