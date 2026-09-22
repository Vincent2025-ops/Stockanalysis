// 本程式作用為：爬取台股全市場收盤資料(前500大成交量)，找出前 10 名技術指標潛力股。
// 並匯出成兩種 CSV 檔案：
// 1. 保留歷史紀錄的檔案 (例如: Stock_TOP10_1140328_1.csv)
// 2. 供桌面端 / GitHub Actions 讀取的固定檔名 (Stock_TOP10.csv)
// 核心特色：
// - 基礎清單採用 TWSE 官方 OpenAPI (開放資料平台)，無連線封鎖限制
// - 歷史指標 (布林通道下軌乖離率、10日均量比值) 統一採用 Yahoo Finance 日K資料
// - 成交量與收盤價強制與 Yahoo 日K同步，徹底解決不同資料源時間軸落差問題
// - 新增：自動往歷史回溯計算 10 日均量比值連續大於 0.5 的天數，並加入說明欄位
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
	StockID       string  // 股票代號 (如: 2330)
	StockName     string  // 股票名稱 (如: 台積電)
	Price         float64 // 今日收盤價 (後續由 Yahoo 日K最新報價覆蓋校正)
	PrevPrice     float64 // 昨收價 (用於計算單日簡易指標)
	Volume        int     // 今日總成交量 (股數，由 Yahoo 日K最新資料覆蓋校正)
	RSI           float64 // 相對強弱指標 (簡易單日估算)
	KD            float64 // 隨機指標 (簡易單日估算)
	MACD          float64 // 平滑異同移動平均線 (簡易單日估算)
	SMA           float64 // 簡單移動平均線 (簡易單日估算)
	Momentum      float64 // 動能指標 (簡易單日估算)
	ChipRatio     float64 // 10日均量比值 (今日成交量 / 前10日均量基準)
	ChipRatioDays int     // 🎯 10日均量比值連續大於 0.5 的天數 (已符合日數)
	Bollinger     float64 // 布林通道下軌乖離率 (負值代表跌破下軌)
	CompanyInfo   string  // 公司資訊 (備用欄位)
}

// =====================================================================
// 2. 核心爬蟲與單日計算邏輯 (TWSE OpenAPI)
// =====================================================================

// fetchStockData 透過 TWSE OpenAPI 抓取全市場股票名單與基礎量價
func fetchStockData() ([]StockData, error) {
	// OpenAPI 端點無嚴格防爬蟲限制，適合在雲端與 GitHub Actions 執行
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

	// OpenAPI 回傳格式為物件陣列 []map[string]string
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

		// OpenAPI 的 Change 欄位已包含正負號 (例如: "+1.50", "-1.50")
		change, _ := strconv.ParseFloat(changeStr, 64)
		prevPrice := price
		if price > 0 {
			prevPrice = price - change
		}

		tempStocks = append(tempStocks, StockData{
			StockID:       stockID,
			StockName:     stockName,
			Price:         price,
			PrevPrice:     prevPrice,
			Volume:        volume,
			ChipRatio:     -1.0,   // 初始化為 -1.0，確保只有成功計算 Yahoo 歷史均量者才能進榜
			ChipRatioDays: 0,      // 初始化連續符合天數為 0
			Bollinger:     9999.0, // 初始化為極大值，未計算者排序置底
		})
	}

	var stocks []StockData
	for _, stock := range tempStocks {
		if stock.Price <= 0 {
			continue // 排除暫停交易或價格無效者
		}
		
		prevPrice := stock.PrevPrice
		if prevPrice <= 0 {
			prevPrice = stock.Price * 0.98
		}

		// 計算單日技術指標
		stock.RSI = calculateRSI(stock.Price, prevPrice)
		stock.KD = calculateKD(stock.Price, prevPrice)
		stock.MACD = calculateMACD(stock.Price, prevPrice)
		stock.SMA = calculateSMA(stock.Price, prevPrice)
		stock.Momentum = calculateMomentum(stock.Price, prevPrice)
		
		stocks = append(stocks, stock)
	}

	return stocks, nil
}

// =====================================================================
// 3. 布林通道與 10 日均量比值歷史計算邏輯 (Yahoo Finance API)
// =====================================================================

// YahooResponse 用於解析 Yahoo Finance Chart API 的日K線資料
type YahooResponse struct {
	Chart struct {
		Result []struct {
			Indicators struct {
				Quote []struct {
					Close  []float64 `json:"close"`  // 歷史收盤價陣列
					Volume []float64 `json:"volume"` // 歷史成交股數陣列
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

// scanBollingerBands 針對成交量前 500 大熱門股，呼叫 Yahoo API 計算布林通道與 10 日均量比值
func scanBollingerBands(stocks []StockData) {
	fmt.Println("📊 啟動歷史指標掃描：開始篩選市場前 500 大熱門股 (以 Yahoo 準確日K校正價格、量能與均量)...")

	// 依初步成交量排序，取前 500 大熱門股
	sortedByVol := make([]StockData, len(stocks))
	copy(sortedByVol, stocks)
	sort.Slice(sortedByVol, func(i, j int) bool {
		return sortedByVol[i].Volume > sortedByVol[j].Volume
	})

	topCount := 500
	if len(sortedByVol) < 500 {
		topCount = len(sortedByVol)
	}
	
	countBollinger := 0
	countChipRatio := 0
	client := &http.Client{Timeout: 10 * time.Second}

	for i := 0; i < topCount; i++ {
		sid := sortedByVol[i].StockID
		// 🎯 調整為 range=3mo (約60個交易日)，提供充足的歷史長度以追溯計算連續符合天數
		url := fmt.Sprintf("https://query2.finance.yahoo.com/v8/finance/chart/%s.TW?range=3mo&interval=1d", sid)
		
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		// 加入瀏覽器標頭防止連線被拒
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		
		var yResp YahooResponse
		err = json.NewDecoder(resp.Body).Decode(&yResp)
		resp.Body.Close()
		
		if err == nil && len(yResp.Chart.Result) > 0 && len(yResp.Chart.Result[0].Indicators.Quote) > 0 {
			quote := yResp.Chart.Result[0].Indicators.Quote[0]
			closes := quote.Close
			vols := quote.Volume

			// 過濾有效收盤價 (排除 null 或 0)
			var validPrices []float64
			for _, p := range closes {
				if p > 0 {
					validPrices = append(validPrices, p)
				}
			}
			
			// 1. 布林通道計算 (嚴格截取最後 20 日對齊布林通道 20MA 標準)
			if len(validPrices) >= 15 {
				pricesForBB := validPrices
				if len(pricesForBB) > 20 {
					pricesForBB = pricesForBB[len(pricesForBB)-20:]
				}
				
				_, _, dn := calculateBollinger(pricesForBB)
				if dn > 0 {
					for j := range stocks {
						if stocks[j].StockID == sid {
							latestP := validPrices[len(validPrices)-1]
							// 乖離率 = (當日收盤價 - 下軌) / 下軌 * 100
							stocks[j].Bollinger = ((latestP - dn) / dn) * 100
							countBollinger++
							break
						}
					}
				}
			}

			// 2. 10日均量比值與「連續符合天數」計算
			var validVols []float64
			for _, v := range vols {
				if v >= 0 {
					validVols = append(validVols, v)
				}
			}
			
			// 至少需有 11 天資料 (今日 1 天 + 前 10 個交易日)
			if len(validVols) >= 11 {
				n := len(validVols)
				todayVol := validVols[n-1] // 當日最新成交股數

				// 嚴格取「今日以前」的 10 個交易日作為平均基準
				prev10Vols := validVols[n-11 : n-1]
				sumV := 0.0
				for _, v := range prev10Vols {
					sumV += v
				}
				avgV := sumV / 10.0

				if avgV > 0 {
					ratio := todayVol / avgV // 當日比值

					// 🎯 核心計算：回溯歷史 K 線，統計比值連續大於 0.5 的天數
					consecutiveDays := 0
					for t := n - 1; t >= 10; t-- {
						// 計算當日 t 過去 10 個交易日的成交量總和
						sumDayV := 0.0
						for _, v := range validVols[t-10 : t] {
							sumDayV += v
						}
						avgDayV := sumDayV / 10.0

						// 若該日比值 > 0.5 則天數累加；一旦小於等於 0.5 立即中斷連續計數
						if avgDayV > 0 && (validVols[t]/avgDayV) > 0.5 {
							consecutiveDays++
						} else {
							break
						}
					}

					for j := range stocks {
						if stocks[j].StockID == sid {
							stocks[j].ChipRatio = ratio
							stocks[j].ChipRatioDays = consecutiveDays // 記錄連續符合天數
							// 強制將收盤價與成交量校正為 Yahoo 最新真實日K資料
							if len(validPrices) > 0 {
								stocks[j].Price = validPrices[len(validPrices)-1]
							}
							stocks[j].Volume = int(todayVol)
							countChipRatio++
							break
						}
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond) // 間隔 100ms 防止高頻請求被擋
	}
	
	fmt.Printf("✅ 分析完成！成功計算 布林通道: %d 檔, 10日均量比值: %d 檔。\n", countBollinger, countChipRatio)
}

// calculateBollinger 依據收盤價序列計算布林通道 (中軌、上軌、下軌)
func calculateBollinger(prices []float64) (mb, up, dn float64) {
	n := float64(len(prices))
	if n == 0 {
		return 0, 0, 0
	}
	
	// 計算中軌 (SMA 均線)
	var sum float64
	for _, p := range prices {
		sum += p
	}
	mb = sum / n

	// 計算標準差
	var variance float64
	for _, p := range prices {
		variance += math.Pow(p-mb, 2)
	}
	sd := math.Sqrt(variance / n)

	// 計算上軌 (+2SD) 與下軌 (-2SD)
	up = mb + (2 * sd)
	dn = mb - (2 * sd)
	return mb, up, dn
}

// 簡易單日指標計算公式
func calculateRSI(current, prev float64) float64      { return 100 - (100 / (1 + (current / prev))) }
func calculateKD(current, prev float64) float64       { return (current - prev) / prev * 100 }
func calculateMACD(current, prev float64) float64     { return current - prev }
func calculateSMA(current, prev float64) float64      { return (current + prev) / 2 }
func calculateMomentum(current, prev float64) float64 { return (current / prev) * 100 }

// =====================================================================
// 4. 排序與匯出邏輯
// =====================================================================

// getTop10 依指定指標對股票進行過濾與排名，回傳前 10 名
func getTop10(stocks []StockData, indicator string) []StockData {
	var candidates []StockData
	for _, s := range stocks {
		// 排除未成功計算 10 日均量比值的標的
		if indicator == "ChipRatio" && s.ChipRatio < 0 {
			continue
		}
		// 排除未成功計算布林通道的標的
		if indicator == "Bollinger" && s.Bollinger > 9000 {
			continue
		}
		candidates = append(candidates, s)
	}

	sort.Slice(candidates, func(i, j int) bool {
		switch indicator {
		case "RSI":
			return candidates[i].RSI < candidates[j].RSI // RSI 由小到大 (超賣反彈)
		case "KD":
			return candidates[i].KD > candidates[j].KD // KD 由大到小 (黃金交叉)
		case "MACD":
			return candidates[i].MACD > candidates[j].MACD // MACD 由大到小
		case "SMA":
			return candidates[i].SMA > candidates[j].SMA // SMA 由大到小
		case "Momentum":
			return candidates[i].Momentum > candidates[j].Momentum // 動能由大到小
		case "ChipRatio":
			return candidates[i].ChipRatio > candidates[j].ChipRatio // 均量比值由大到小 (放量突破)
		case "Bollinger":
			return candidates[i].Bollinger < candidates[j].Bollinger // 負乖離越大 (跌破下軌) 越優先
		}
		return false
	})

	if len(candidates) > 10 {
		return candidates[:10]
	}
	return candidates
}

// exportToCSV 將 7 大指標計算結果依格式輸出成 CSV 檔案
func exportToCSV(fileName string, allTop10 map[string][]StockData) error {
	file, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("❌ 無法建立 CSV: %v", err)
	}
	defer file.Close()

	file.WriteString("\xEF\xBB\xBF") // 寫入 UTF-8 BOM 避免 Excel 開啟亂碼
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 寫入 CSV 標題列
	writer.Write([]string{"技術指標", "股票代號", "名稱", "價格", "成交量", "指標值", "說明"})
	order := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "ChipRatio", "Bollinger"}

	for _, indicator := range order {
		stocks, ok := allTop10[indicator]
		if !ok || len(stocks) == 0 {
			writer.Write([]string{indicator, "-", "-", "-", "-", "-", "今日無符合條件個股"})
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
				// 🎯 加入「已連續符合 %d 日」動態說明
				desc = fmt.Sprintf("籌碼集中度提升，顯示主力介入 (10 日均量比值大於 0.5 時買進，小於 0.5 時平倉)，目前指標數值：%.2f (已連續符合 %d 日)", stock.ChipRatio, stock.ChipRatioDays)
			case "Bollinger":
				if stock.Bollinger > 5.0 {
					continue
				}
				valueStr = fmt.Sprintf("%.2f%%", stock.Bollinger)
				if stock.Bollinger < 0 {
					desc = "💥 跌破布林下軌，短線具備極高超跌反彈潛力"
				} else {
					desc = "貼近布林下軌，落入超賣區間"
				}
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
// 5. 主執行函式
// =====================================================================

func main() {
	fmt.Println("=== 🚀 開始執行台股 7 大策略掃描器 ===")
	
	// 步驟 1：取得全市場當日基礎清單
	stocks, err := fetchStockData()
	if err != nil {
		fmt.Println("❌ 抓取資料失敗:", err)
		return
	}

	// 步驟 2：針對前 500 大個股進行歷史 K 線深度掃描 (布林通道 + 均量比值校正 + 連續天數計算)
	scanBollingerBands(stocks)

	// 步驟 3：依各指標排序取 Top 10
	indicators := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "ChipRatio", "Bollinger"}
	allTop10 := make(map[string][]StockData)

	for _, ind := range indicators {
		stocksCopy := make([]StockData, len(stocks))
		copy(stocksCopy, stocks)
		
		top10 := getTop10(stocksCopy, ind)
		allTop10[ind] = top10
	}

	// 步驟 4：建立民國年日期檔名並匯出備份與固定檔名
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

	exportToCSV(fileName, allTop10)
	exportToCSV("Stock_TOP10.csv", allTop10)
	
	fmt.Println("🎉 全市場掃描任務完成！")
}