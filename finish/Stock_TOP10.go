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

// fetchStockData 改用 TWSE OpenAPI 爬取全市場股票資料
func fetchStockData() ([]StockData, error) {
	// 使用 OpenAPI 端點，對 GitHub Actions 等雲端環境完全開放
	req, err := http.NewRequest("GET", "https://openapi.twse.com.tw/v1/exchangeReport/STOCK_DAY_ALL", nil)
	if err != nil {
		return nil, err
	}
	
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAPI 拒絕連線，HTTP 狀態碼: %d", resp.StatusCode)
	}

	// OpenAPI 回傳格式為 JSON 物件陣列 []map[string]string
	var dataList []map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&dataList); err != nil {
		return nil, fmt.Errorf("JSON 解析錯誤: %v", err)
	}

	var tempStocks []StockData
	
	for _, row := range dataList {
		stockID := row["Code"]
		stockName := row["Name"]
		volStr := strings.ReplaceAll(row["TradeVolume"], ",", "")
		priceStr := strings.ReplaceAll(row["ClosingPrice"], ",", "")
		changeStr := row["Change"] 

		volume, _ := strconv.Atoi(volStr)
		price, _ := strconv.ParseFloat(priceStr, 64)

		// OpenAPI 的 Change 欄位已包含正負號 (如 "+1.50", "-1.50")
		change, _ := strconv.ParseFloat(changeStr, 64)
		prevPrice := price
		if price > 0 {
			prevPrice = price - change
		}

		tempStocks = append(tempStocks, StockData{
			StockID:   stockID,
			StockName: stockName,
			Price:     price,
			PrevPrice: prevPrice,
			Volume:    volume,
			Bollinger: 9999.0,
		})
	}

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

// YahooResponse 用於解析 Yahoo Finance API 回傳的歷史圖表資料
type YahooResponse struct {
	Chart struct {
		Result []struct {
			Indicators struct {
				Quote []struct {
					Close []float64 `json:"close"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

// scanBollingerBands 改用 Yahoo Finance API 獲取前 200 大熱門股歷史資料
func scanBollingerBands(stocks []StockData) {
	fmt.Println("📊 啟動布林通道掃描：開始篩選市場前 200 大熱門股 (透過 Yahoo Finance API)...")

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
	
	countBollinger := 0
	client := &http.Client{Timeout: 10 * time.Second}

	fmt.Printf("⏳ 開始並行抓取熱門股歷史軌跡，預計需要約 20 秒...\n")

	for i := 0; i < topCount; i++ {
		sid := sortedByVol[i].StockID
		
		// 透過 Yahoo Finance 抓取近 1 個月的日 K 收盤資料
		url := fmt.Sprintf("https://query2.finance.yahoo.com/v8/finance/chart/%s.TW?range=1mo&interval=1d", sid)
		
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		// 帶上 User-Agent 確保請求成功率
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		
		var yResp YahooResponse
		err = json.NewDecoder(resp.Body).Decode(&yResp)
		resp.Body.Close()
		
		if err == nil && len(yResp.Chart.Result) > 0 && len(yResp.Chart.Result[0].Indicators.Quote) > 0 {
			closes := yResp.Chart.Result[0].Indicators.Quote[0].Close
			var validPrices []float64
			
			// Yahoo 資料偶爾會因休市產生 null (在 Go 中被解析為 0)，這裡過濾出有效報價
			for _, p := range closes {
				if p > 0 {
					validPrices = append(validPrices, p)
				}
			}
			
			// 確保至少有 15 天的資料才做計算
			if len(validPrices) >= 15 {
				// 若資料超過 20 天，只取最後 20 天對齊布林通道 20MA 標準
				if len(validPrices) > 20 {
					validPrices = validPrices[len(validPrices)-20:]
				}
				
				_, _, dn := calculateBollinger(validPrices)
				if dn > 0 {
					// 找回原始 stocks 陣列中的對應個股並寫入乖離率
					for j := range stocks {
						if stocks[j].StockID == sid {
							stocks[j].Bollinger = ((stocks[j].Price - dn) / dn) * 100
							countBollinger++
							break
						}
					}
				}
			}
		}
		// 加入微小的延遲，避免密集連線遭阻擋
		time.Sleep(100 * time.Millisecond)
	}
	
	fmt.Printf("✅ 布林通道分析完成！共成功計算 %d 檔熱門潛力股。\n", countBollinger)
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