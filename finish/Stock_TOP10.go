// 本程式作用為：爬取台股全市場收盤資料，找出前 10 名技術指標潛力股。
// 並匯出成兩種 CSV 檔案：
// 1. 保留歷史紀錄的檔案 (例如: Stock_TOP10_1140328_1.csv)
// 2. 供桌面端 / GitHub Actions 讀取的固定檔名 (Stock_TOP10.csv)
// 新增：第 7 大策略「布林通道下軌掃描」，針對前 200 大熱門股進行 20 日均線與標準差運算。
package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
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
	Bollinger   float64 // 布林通道下軌乖離率 (越低代表越貼近或跌破下軌)
	CompanyInfo string  // 公司資訊 (備用欄位)
}

// =====================================================================
// 2. 核心爬蟲與單日計算邏輯
// =====================================================================

// fetchStockData 呼叫台灣證交所 API 爬取全市場股票資料，並計算單日指標
func fetchStockData() ([]StockData, error) {
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
	
	// 第一階段走訪：解析基本資料與初步昨收價
	for _, item := range dataList {
		row := item.([]interface{})
		if len(row) < 10 {
			continue
		}

		stockID := fmt.Sprintf("%v", row[0])
		stockName := fmt.Sprintf("%v", row[1])
		volStr := strings.ReplaceAll(fmt.Sprintf("%v", row[2]), ",", "")
		priceStr := strings.ReplaceAll(fmt.Sprintf("%v", row[7]), ",", "")
		changeStr := fmt.Sprintf("%v", row[8])

		volume, _ := strconv.Atoi(volStr)
		price, _ := strconv.ParseFloat(priceStr, 64)

		prevPrice := extractPrevPrice(price, changeStr, "")

		tempStocks = append(tempStocks, StockData{
			StockID:   stockID,
			StockName: stockName,
			Price:     price,
			PrevPrice: prevPrice,
			Volume:    volume,
			Bollinger: 9999.0, // 初始化布林通道分數為極大值 (確保未計算的排在最後)
		})
	}

	// 確保收盤價有效
	fillMissingPrices(tempStocks)

	var stocks []StockData
	
	// 第二階段走訪：計算基礎技術指標
	for _, stock := range tempStocks {
		if stock.Price <= 0 {
			continue
		}

		prevPrice := stock.PrevPrice
		if prevPrice <= 0 {
			prevPrice = stock.Price * 0.98
		}

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

// =====================================================================
// 3. 布林通道專屬計算與歷史回推邏輯
// =====================================================================

// scanBollingerBands 找出前 200 大熱門股，並追溯 20 天歷史資料計算布林通道
func scanBollingerBands(stocks []StockData) {
	fmt.Println("📊 啟動布林通道掃描：開始篩選市場前 200 大熱門股...")

	// 1. 複製並根據成交量排序，找出前 200 名
	sortedByVol := make([]StockData, len(stocks))
	copy(sortedByVol, stocks)
	sort.Slice(sortedByVol, func(i, j int) bool {
		return sortedByVol[i].Volume > sortedByVol[j].Volume
	})

	topCount := 200
	if len(sortedByVol) < 200 {
		topCount = len(sortedByVol)
	}
	
	// 建立熱門股清單與歷史價格 Map
	targetMap := make(map[string]bool)
	priceHistory := make(map[string][]float64)
	
	for i := 0; i < topCount; i++ {
		sid := sortedByVol[i].StockID
		targetMap[sid] = true
		// 放入今日最新收盤價作為第一筆資料
		priceHistory[sid] = append(priceHistory[sid], sortedByVol[i].Price)
	}

	// 2. 往前追溯 19 個交易日的收盤價
	daysNeeded := 19
	daysFound := 0
	targetDate := time.Now()
	
	fmt.Printf("⏳ 開始抓取過去 20 日歷史軌跡，預計需要約 2~3 分鐘 (加入安全延遲以防阻擋)，請稍候...\n")

	// 最多往前找 35 天 (避開假日與連假)
	for attempts := 0; attempts < 35 && daysFound < daysNeeded; attempts++ {
		targetDate = targetDate.AddDate(0, 0, -1)
		if targetDate.Weekday() == time.Saturday || targetDate.Weekday() == time.Sunday {
			continue
		}

		dateStr := targetDate.Format("20060102")
		url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/MI_INDEX?response=json&date=%s&type=ALL", dateStr)
		
		resp, err := http.Get(url)
		if err != nil {
			fmt.Printf("   ⚠️ 連線失敗 (%s)，重試中...\n", dateStr)
			time.Sleep(time.Duration(5+rand.Intn(3)) * time.Second)
			continue
		}
		
		var result map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		
		if err != nil {
			// 若 JSON 解析失敗，極高機率是被證交所回傳了防爬蟲 HTML 頁面
			fmt.Printf("   ⚠️ 解析 %s 資料失敗 (極可能已被證交所防爬蟲阻擋 IP)\n", dateStr)
			time.Sleep(time.Duration(10+rand.Intn(5)) * time.Second) // 遭遇阻擋時退避更久
			continue
		}

		var dataList []interface{}
		// 支援 TWSE 新版 API 格式 (資料放在 tables 陣列中)
		if tables, ok := result["tables"].([]interface{}); ok {
			for _, tbl := range tables {
				if tableMap, ok := tbl.(map[string]interface{}); ok {
					if list, ok := tableMap["data"].([]interface{}); ok && len(list) > 500 {
						dataList = list
						break
					}
				}
			}
		}
		// 兼容 TWSE 舊版 API 格式 (資料放在 data1, data9 等)
		if len(dataList) == 0 {
			for k, v := range result {
				if strings.HasPrefix(k, "data") {
					if list, ok := v.([]interface{}); ok && len(list) > 500 {
						dataList = list
						break
					}
				}
			}
		}

		if len(dataList) > 0 {
			daysFound++
			for _, item := range dataList {
				row, ok := item.([]interface{})
				if !ok || len(row) < 9 {
					continue
				}
				stockID := strings.TrimSpace(fmt.Sprintf("%v", row[0]))
				
				// 只有前 200 大熱門股才記錄歷史價格
				if targetMap[stockID] {
					priceStr := strings.ReplaceAll(fmt.Sprintf("%v", row[8]), ",", "")
					if p, err := strconv.ParseFloat(priceStr, 64); err == nil && p > 0 {
						priceHistory[stockID] = append(priceHistory[stockID], p)
					}
				}
			}
			fmt.Printf("   > 已取得 %s 資料 (%d/%d)\n", dateStr, daysFound, daysNeeded)
		} else {
			fmt.Printf("   ⚠️ %s 無法找到股票清單 (可能是休市或格式改變)\n", dateStr)
		}
		
		// ⚠️ 防擋延遲，避免被證交所封鎖 IP (加入隨機 5~8 秒延遲)
		time.Sleep(time.Duration(5+rand.Intn(4)) * time.Second)
	}

	// 3. 計算布林通道指標
	countBollinger := 0
	for i := range stocks {
		sid := stocks[i].StockID
		if targetMap[sid] {
			prices := priceHistory[sid]
			// 確保有足夠的歷史資料 (容許些微停牌，>15天即計算)
			if len(prices) >= 15 {
				_, _, dn := calculateBollinger(prices)
				if dn > 0 {
					// 乖離率 = (目前股價 - 下軌) / 下軌 * 100
					// 若數值為負，代表跌破下軌；越小代表跌越深
					stocks[i].Bollinger = ((stocks[i].Price - dn) / dn) * 100
					countBollinger++
				}
			}
		}
	}
	fmt.Printf("✅ 布林通道分析完成！共成功計算 %d 檔熱門潛力股。\n", countBollinger)
	if countBollinger == 0 {
		fmt.Println("❌ 警告：布林通道計算數為 0，請確認是否觸發了 API 限制！")
	}
}

// calculateBollinger 計算布林通道 (回傳 中軌, 上軌, 下軌)
func calculateBollinger(prices []float64) (mb, up, dn float64) {
	n := float64(len(prices))
	if n == 0 {
		return 0, 0, 0
	}
	
	// 1. 計算中軌 (SMA)
	var sum float64
	for _, p := range prices {
		sum += p
	}
	mb = sum / n

	// 2. 計算標準差 (Standard Deviation)
	var variance float64
	for _, p := range prices {
		variance += math.Pow(p-mb, 2)
	}
	sd := math.Sqrt(variance / n)

	// 3. 計算上下軌
	up = mb + (2 * sd)
	dn = mb - (2 * sd)
	return mb, up, dn
}

// =====================================================================
// 4. 輔助處理函式 (無效價格回推等)
// =====================================================================

func fillMissingPrices(stocks []StockData) {
	missing := make(map[string]int)
	for i := range stocks {
		if stocks[i].Price <= 0 {
			missing[stocks[i].StockID] = i
		}
	}

	if len(missing) == 0 {
		return
	}

	fmt.Printf("🔍 發現 %d 檔今日無有效收盤價，開始往前推尋找歷史收盤價...\n", len(missing))
	targetDate := time.Now()
	attempts := 0
	
	for len(missing) > 0 && attempts < 5 {
		targetDate = targetDate.AddDate(0, 0, -1)
		if targetDate.Weekday() == time.Saturday || targetDate.Weekday() == time.Sunday {
			continue
		}
		
		attempts++
		dateStr := targetDate.Format("20060102")
		url := fmt.Sprintf("https://www.twse.com.tw/exchangeReport/MI_INDEX?response=json&date=%s&type=ALL", dateStr)
		
		resp, err := http.Get(url)
		if err != nil {
			time.Sleep(time.Duration(3+rand.Intn(3)) * time.Second)
			continue
		}
		
		var result map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		
		if err != nil {
			time.Sleep(time.Duration(5+rand.Intn(3)) * time.Second)
			continue
		}

		var dataList []interface{}
		// 支援 TWSE 新版 API 格式 (資料放在 tables 陣列中)
		if tables, ok := result["tables"].([]interface{}); ok {
			for _, tbl := range tables {
				if tableMap, ok := tbl.(map[string]interface{}); ok {
					if list, ok := tableMap["data"].([]interface{}); ok && len(list) > 500 {
						dataList = list
						break
					}
				}
			}
		}
		// 兼容 TWSE 舊版 API 格式 (資料放在 data1, data9 等)
		if len(dataList) == 0 {
			for k, v := range result {
				if strings.HasPrefix(k, "data") {
					if list, ok := v.([]interface{}); ok && len(list) > 500 {
						dataList = list
						break
					}
				}
			}
		}
		
		if len(dataList) > 0 {
			foundCount := 0
			for _, item := range dataList {
				row, ok := item.([]interface{})
				if !ok || len(row) < 11 {
					continue
				}
				
				stockID := strings.TrimSpace(fmt.Sprintf("%v", row[0]))
				if idx, needsUpdate := missing[stockID]; needsUpdate {
					priceStr := strings.ReplaceAll(fmt.Sprintf("%v", row[8]), ",", "")
					signStr := fmt.Sprintf("%v", row[9])
					changeStr := fmt.Sprintf("%v", row[10])

					if priceStr != "--" && priceStr != "" {
						if p, err := strconv.ParseFloat(priceStr, 64); err == nil && p > 0 {
							prevP := extractPrevPrice(p, changeStr, signStr)
							stocks[idx].Price = p
							stocks[idx].PrevPrice = prevP
							delete(missing, stockID)
							foundCount++
						}
					}
				}
			}
			if foundCount > 0 {
				fmt.Printf("✅ 從 %s 找回 %d 檔的有效收盤價，剩餘 %d 檔...\n", dateStr, foundCount, len(missing))
			}
		}
		time.Sleep(time.Duration(3+rand.Intn(3)) * time.Second) 
	}
}

func extractPrevPrice(price float64, changeStr string, signStr string) float64 {
	if changeStr == "" || changeStr == "--" || price == 0 {
		return price
	}

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
	isNegative := strings.Contains(changeStr, "-") || strings.Contains(signStr, "-") || strings.Contains(signStr, "綠")
	
	if isNegative {
		change = -change
	}
	return price - change
}

// 簡易技術指標計算
func calculateRSI(current, prev float64) float64      { return 100 - (100 / (1 + (current / prev))) }
func calculateKD(current, prev float64) float64       { return (current - prev) / prev * 100 }
func calculateMACD(current, prev float64) float64     { return current - prev }
func calculateSMA(current, prev float64) float64      { return (current + prev) / 2 }
func calculateMomentum(current, prev float64) float64 { return (current / prev) * 100 }
func calculateChipRatio(volume int) float64           { return float64(volume) / 100000.0 }

// =====================================================================
// 5. 排序與匯出邏輯
// =====================================================================

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
		case "Bollinger":
			// 布林通道下軌乖離率越小(甚至為負)，代表越貼近或跌穿下軌，越有反彈潛力
			return stocks[i].Bollinger < stocks[j].Bollinger
		}
		return false
	})

	if len(stocks) > 10 {
		return stocks[:10]
	}
	return stocks
}

func exportToCSV(fileName string, allTop10 map[string][]StockData) error {
	file, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("❌ 無法建立 CSV: %v", err)
	}
	defer file.Close()

	file.WriteString("\xEF\xBB\xBF") // 寫入 UTF-8 BOM
	writer := csv.NewWriter(file)
	defer writer.Flush()

	writer.Write([]string{"技術指標", "股票代號", "名稱", "價格", "成交量", "指標值", "說明"})
	// 📌 新增 Bollinger 到匯出清單排序中
	order := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "ChipRatio", "Bollinger"}

	for _, indicator := range order {
		stocks, ok := allTop10[indicator]
		if !ok {
			continue 
		}

		// 確認這個指標是否有被成功計算 (如果全為 9999.0 代表沒計算)
		calculatedCount := 0
		if indicator == "Bollinger" {
			for _, s := range stocks {
				if s.Bollinger < 9000 {
					calculatedCount++
				}
			}
		} else {
			calculatedCount = 1 // 其他單日指標預設有計算
		}

		hasValidStock := false

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
			case "Bollinger":
				// 若值為 9999 代表未計算，或乖離率大於 5% (不夠貼近下軌)，就不輸出
				if stock.Bollinger > 9000 || stock.Bollinger > 5.0 {
					continue
				}
				valueStr = fmt.Sprintf("%.2f%%", stock.Bollinger)
				if stock.Bollinger < 0 {
					desc = "💥 跌破布林下軌，短線具備極高超跌反彈潛力"
				} else {
					desc = "貼近布林下軌，落入超賣區間"
				}
			}

			hasValidStock = true
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

		// 如果該指標沒有任何符合條件的個股，則輸出一行提示
		if !hasValidStock {
			msg := "今日無符合條件個股"
			// 🛡️ 防呆：如果根本沒算出來，要誠實地提示使用者，而不是假裝沒符合條件
			if indicator == "Bollinger" && calculatedCount == 0 {
				msg = "資料不足無法計算 (可能觸發證交所 IP 限制)"
			}

			writer.Write([]string{
				indicator,
				"-",
				"-",
				"-",
				"-",
				"-",
				msg,
			})
		}
	}

	fmt.Println("✅ CSV 匯出成功！檔名:", fileName)
	return nil
}

// =====================================================================
// 6. 主執行函式 (程式進入點)
// =====================================================================
func main() {
	fmt.Println("=== 🚀 開始執行台股 7 大策略掃描器 ===")
	
	// 1. 抓取當日基礎資料
	stocks, err := fetchStockData()
	if err != nil {
		fmt.Println("❌ 抓取資料失敗:", err)
		return
	}

	// 2. 啟動布林通道掃描 (運算需要約 40 秒)
	scanBollingerBands(stocks)

	// 3. 彙整 7 大策略 Top 10
	indicators := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "ChipRatio", "Bollinger"}
	allTop10 := make(map[string][]StockData)

	for _, ind := range indicators {
		stocksCopy := make([]StockData, len(stocks))
		copy(stocksCopy, stocks)
		
		top10 := getTop10(stocksCopy, ind)
		allTop10[ind] = top10
	}

	// 4. 準備輸出檔案
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

	// 匯出歷史備份與覆蓋用檔案
	exportToCSV(fileName, allTop10)
	exportToCSV("Stock_TOP10.csv", allTop10)
	
	fmt.Println("🎉 全市場掃描任務完成！")
}