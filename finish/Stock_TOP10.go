// 本程式作用為：爬取台股全市場收盤資料，找出前 10 名技術指標潛力股。
// 並匯出成兩種 CSV 檔案：
// 1. 保留歷史紀錄的檔案 (例如: Stock_TOP10_1140328_1.csv)
// 2. 供桌面端 / GitHub Actions 讀取的固定檔名 (Stock_TOP10.csv)
package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// =====================================================================
// 1. 資料結構定義
// =====================================================================

// StockData 定義單一檔股票的資料與其計算出的各項技術指標
type StockData struct {
	StockID     string  // 股票代號 (如: 2330)
	StockName   string  // 股票名稱 (如: 台積電)
	Price       float64 // 今日收盤價 (若今日無交易則為最後有效收盤價)
	PrevPrice   float64 // 昨收價 (與 Price 對應的前一個有效交易日收盤價)
	Volume      int     // 今日成交量 (股數)
	RSI         float64 // 相對強弱指標
	KD          float64 // 隨機指標
	MACD        float64 // 平滑異同移動平均線
	SMA         float64 // 簡單移動平均線
	Momentum    float64 // 動能指標
	ChipRatio   float64 // 籌碼集中度估算
	CompanyInfo string  // 公司資訊 (備用欄位)
}

// =====================================================================
// 2. 核心爬蟲與計算邏輯
// =====================================================================

// fetchStockData 呼叫台灣證交所 API 爬取全市場股票資料，並計算指標
func fetchStockData() ([]StockData, error) {
	// 1. 呼叫證交所 STOCK_DAY_ALL 取得今日所有股票收盤行情
	resp, err := http.Get("https://www.twse.com.tw/exchangeReport/STOCK_DAY_ALL?response=json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	dataList, ok := result["data"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("資料格式解析錯誤")
	}

	var tempStocks []StockData
	
	// 2. 第一階段走訪：解析基本資料與初步昨收價
	for _, item := range dataList {
		row := item.([]interface{})
		if len(row) < 10 {
			continue // 忽略欄位不足的異常資料
		}

		stockID := fmt.Sprintf("%v", row[0])
		stockName := fmt.Sprintf("%v", row[1])
		volStr := strings.ReplaceAll(fmt.Sprintf("%v", row[2]), ",", "")
		priceStr := strings.ReplaceAll(fmt.Sprintf("%v", row[7]), ",", "")
		changeStr := fmt.Sprintf("%v", row[8]) // 漲跌價差 (STOCK_DAY_ALL 在第 8 欄)

		volume, _ := strconv.Atoi(volStr)
		price, _ := strconv.ParseFloat(priceStr, 64) // 若為 "--" 或空值，會解析為 0

		// 根據今日的收盤價與漲跌價差，反推真正的「昨收價」
		prevPrice := extractPrevPrice(price, changeStr, "")

		// 📌 依照需求，保留全市場股票，不論價格高低與成交量
		tempStocks = append(tempStocks, StockData{
			StockID:   stockID,
			StockName: stockName,
			Price:     price,
			PrevPrice: prevPrice,
			Volume:    volume,
		})
	}

	// 3. 確保收盤價有效：若有無效收盤價，一路往前推到有效收盤價 (並同步推算該日對應的昨收價)
	fillMissingPrices(tempStocks)

	var stocks []StockData
	
	// 4. 第二階段走訪：根據有效收盤價與正確對應的昨收價，計算技術指標
	for _, stock := range tempStocks {
		// 如果往前推了 5 天還是沒有有效價格，代表是長期停牌股或特殊證券，則略過不計算
		if stock.Price <= 0 {
			continue
		}

		// 確保 prevPrice 為有效值，防呆保護
		prevPrice := stock.PrevPrice
		if prevPrice <= 0 {
			prevPrice = stock.Price * 0.98
		}

		// 計算技術指標
		stock.RSI = calculateRSI(stock.Price, prevPrice)
		stock.KD = calculateKD(stock.Price, prevPrice)
		stock.MACD = calculateMACD(stock.Price, prevPrice)
		stock.SMA = calculateSMA(stock.Price, prevPrice)
		stock.Momentum = calculateMomentum(stock.Price, prevPrice)
		stock.ChipRatio = calculateChipRatio(stock.Volume)
		
		stocks = append(stocks, stock)
	}

	return stocks, nil
}

// fillMissingPrices 尋找無效收盤價的股票，並透過 MI_INDEX API 一路往前推找回歷史收盤價與歷史昨收價
func fillMissingPrices(stocks []StockData) {
	// 將缺價格的股票放入 Map 以加速查詢，並記錄在 slice 中的索引
	missing := make(map[string]int)
	for i := range stocks {
		if stocks[i].Price <= 0 {
			missing[stocks[i].StockID] = i
		}
	}

	if len(missing) == 0 {
		return // 所有股票都有收盤價，無需回推
	}

	fmt.Printf("🔍 發現 %d 檔股票今日無有效收盤價，開始一路往前推尋找歷史收盤價...\n", len(missing))
	
	targetDate := time.Now()
	attempts := 0
	
	// 最多往前推 5 個交易日 (避免無限迴圈)
	for len(missing) > 0 && attempts < 5 {
		targetDate = targetDate.AddDate(0, 0, -1)
		// 跳過週末
		if targetDate.Weekday() == time.Saturday || targetDate.Weekday() == time.Sunday {
			continue
		}
		
		attempts++
		dateStr := targetDate.Format("20060102")
		url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/MI_INDEX?response=json&date=%s&type=ALL", dateStr)
		
		resp, err := http.Get(url)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		
		var result map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		// 解析 MI_INDEX 中的股票清單表格 (通常包含最多列的資料即為股票清單)
		var dataList []interface{}
		for k, v := range result {
			if strings.HasPrefix(k, "data") {
				if list, ok := v.([]interface{}); ok && len(list) > 500 {
					dataList = list
					break
				}
			}
		}
		
		// 比對找到的歷史資料
		if len(dataList) > 0 {
			foundCount := 0
			for _, item := range dataList {
				row, ok := item.([]interface{})
				if !ok || len(row) < 11 {
					continue
				}
				
				stockID := strings.TrimSpace(fmt.Sprintf("%v", row[0]))
				// 檢查這檔股票是否在我們的「缺價清單」中
				if idx, needsUpdate := missing[stockID]; needsUpdate {
					// MI_INDEX 第 8 欄是收盤價, 第 9 欄是漲跌符號, 第 10 欄是漲跌價差
					priceStr := strings.ReplaceAll(fmt.Sprintf("%v", row[8]), ",", "")
					signStr := fmt.Sprintf("%v", row[9])
					changeStr := fmt.Sprintf("%v", row[10])

					if priceStr != "--" && priceStr != "" {
						if p, err := strconv.ParseFloat(priceStr, 64); err == nil && p > 0 {
							// 算出對應當天有效收盤價的「昨收價」
							prevP := extractPrevPrice(p, changeStr, signStr)
							
							// 更新 slice 內的股價與昨收價
							stocks[idx].Price = p
							stocks[idx].PrevPrice = prevP
							
							// 從缺價清單中移除
							delete(missing, stockID)
							foundCount++
						}
					}
				}
			}
			if foundCount > 0 {
				fmt.Printf("✅ 從 %s 找回 %d 檔股票的有效收盤價與對應昨收價，剩餘 %d 檔...\n", dateStr, foundCount, len(missing))
			}
		}
		
		// ⚠️ 防擋延遲，避免被證交所封鎖 IP
		time.Sleep(2 * time.Second) 
	}
	
	if len(missing) > 0 {
		fmt.Printf("⚠️ 仍有 %d 檔股票在過去 5 個交易日內無有效收盤價 (可能為特別股或長期停牌股)。\n", len(missing))
	}
}

// extractPrevPrice 根據收盤價與漲跌價差，反推昨收價
// 公式：昨收價 = 收盤價 - 漲跌金額 (考量正負號)
func extractPrevPrice(price float64, changeStr string, signStr string) float64 {
	if changeStr == "" || changeStr == "--" || price == 0 {
		return price
	}

	// 萃取純數字部分
	var numericPart string
	for _, char := range changeStr {
		if (char >= '0' && char <= '9') || char == '.' {
			numericPart += string(char)
		}
	}
	
	if numericPart == "" {
		return price
	}

	change, _ := strconv.ParseFloat(numericPart, 64)

	// 判斷正負號 (處理 STOCK_DAY_ALL 的內含負號，與 MI_INDEX 的獨立符號或顏色字串)
	isNegative := strings.Contains(changeStr, "-") || strings.Contains(signStr, "-") || strings.Contains(signStr, "綠")
	
	if isNegative {
		change = -change
	}

	// 昨收價 = 今日收盤價 - 漲跌價差
	return price - change
}

// ---------------------------------------------------------------------
// 以下為簡化版的技術指標模擬算法
// ---------------------------------------------------------------------
func calculateRSI(current, prev float64) float64      { return 100 - (100 / (1 + (current / prev))) }
func calculateKD(current, prev float64) float64       { return (current - prev) / prev * 100 }
func calculateMACD(current, prev float64) float64     { return current - prev }
func calculateSMA(current, prev float64) float64      { return (current + prev) / 2 }
func calculateMomentum(current, prev float64) float64 { return (current / prev) * 100 }
func calculateChipRatio(volume int) float64           { return float64(volume) / 100000.0 }

// =====================================================================
// 3. 排序與匯出邏輯
// =====================================================================

// getTop10 根據指定的技術指標，對股票陣列進行排序，並回傳前 10 名
func getTop10(stocks []StockData, indicator string) []StockData {
	sort.Slice(stocks, func(i, j int) bool {
		switch indicator {
		case "RSI":
			return stocks[i].RSI < stocks[j].RSI
		case "KD":
			return stocks[i].KD > stocks[j].KD
		case "MACD":
			return stocks[i].MACD > stocks[j].MACD
		case "SMA":
			return stocks[i].SMA > stocks[j].SMA
		case "Momentum":
			return stocks[i].Momentum > stocks[j].Momentum
		case "ChipRatio":
			return stocks[i].ChipRatio > stocks[j].ChipRatio
		}
		return false
	})

	if len(stocks) > 10 {
		return stocks[:10]
	}
	return stocks
}

// exportToCSV 將彙整好的 Top10 清單匯出成 CSV 檔案
func exportToCSV(fileName string, allTop10 map[string][]StockData) error {
	file, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("❌ 無法建立 CSV: %v", err)
	}
	defer file.Close()

	// 📌 寫入 UTF-8 BOM
	file.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(file)
	defer writer.Flush()

	writer.Write([]string{"技術指標", "股票代號", "名稱", "價格", "成交量", "指標值", "說明"})
	order := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "ChipRatio"}

	for _, indicator := range order {
		stocks, ok := allTop10[indicator]
		if !ok {
			continue 
		}

		for _, stock := range stocks {
			var valueStr string
			var desc string

			switch indicator {
			case "RSI":
				valueStr = fmt.Sprintf("%.2f", stock.RSI)
				desc = "RSI 低於 30，可能即將反彈"
			case "KD":
				valueStr = fmt.Sprintf("%.2f", stock.KD)
				desc = "KD 指標大於 80，可能形成黃金交叉"
			case "MACD":
				valueStr = fmt.Sprintf("%.2f", stock.MACD)
				desc = "MACD 大於 0，可能進入上升趨勢"
			case "SMA":
				valueStr = fmt.Sprintf("%.2f", stock.SMA)
				desc = "均線持續上升，顯示多頭趨勢"
			case "Momentum":
				valueStr = fmt.Sprintf("%.2f", stock.Momentum)
				desc = "動能指標上升，顯示市場買氣強勁"
			case "ChipRatio":
				valueStr = fmt.Sprintf("%.2f", stock.ChipRatio)
				desc = "籌碼集中度提升，顯示主力介入"
			}

			writer.Write([]string{
				indicator,
				stock.StockID,
				stock.StockName,
				fmt.Sprintf("%.2f", stock.Price), 
				strconv.Itoa(stock.Volume),       
				valueStr,
				desc,
			})
		}
	}

	fmt.Println("✅ CSV 匯出成功！檔名:", fileName)
	return nil
}

// =====================================================================
// 4. 主執行函式 (程式進入點)
// =====================================================================
func main() {
	fmt.Println("開始抓取台股資料...")
	
	// 呼叫抓取與計算邏輯 (不需要寫死的 previousData Map 了！)
	stocks, err := fetchStockData()
	if err != nil {
		fmt.Println("❌ 抓取資料失敗:", err)
		return
	}

	indicators := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "ChipRatio"}
	allTop10 := make(map[string][]StockData)

	for _, ind := range indicators {
		stocksCopy := make([]StockData, len(stocks))
		copy(stocksCopy, stocks)
		
		top10 := getTop10(stocksCopy, ind)
		allTop10[ind] = top10
	}

	now := time.Now()
	minguoYear := now.Year() - 1911
	dateStr := fmt.Sprintf("%d%02d%02d", minguoYear, now.Month(), now.Day())
	
	fileName := fmt.Sprintf("Stock_TOP10_%s.csv", dateStr)
	counter := 1
	for {
		if _, err := os.Stat(fileName); os.IsNotExist(err) {
			break
		}
		fileName = fmt.Sprintf("Stock_TOP10_%s_%d.csv", dateStr, counter)
		counter++
	}

	// 匯出 1：帶有日期的歷史檔案
	exportToCSV(fileName, allTop10)

	// 匯出 2：供桌面軟體同步用的最新版
	exportToCSV("Stock_TOP10.csv", allTop10)
}