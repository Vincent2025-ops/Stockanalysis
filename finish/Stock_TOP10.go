// 本程式作用為：爬取台股全市場收盤資料(前500大成交量)，找出前 10 名技術指標潛力股。
// 並匯出成兩種 CSV 檔案：
// 1. 保留歷史紀錄的檔案 (例如: Stock_TOP10_1140328_1.csv)
// 2. 供桌面端 / GitHub Actions 讀取的固定檔名 (Stock_TOP10.csv)
// 核心特色：
// - 基礎清單採用 TWSE 官方 OpenAPI (開放資料平台)
// - 三大法人日報採用 TWSE 官方最新日結資料 (支援盤中/假日自動回溯最近交易日)
// - 歷史指標 (RSI, KD, MACD, SMA, Momentum, Volume Ratio, 布林通道) 統一採用 Yahoo Finance 真實歷史日K
// - 🎯 全指標升級：所有指標均統計「已連續符合 xx 日」
// - 🎯 價格買點分流：各指標依交易哲學量身打造價格買點基準，並「買點優先排序」
package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
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
	StockID             string  // 股票代號 (如: 2330)
	StockName           string  // 股票名稱 (如: 台積電)
	Price               float64 // 今日收盤價 (由 Yahoo 日K最新報價覆蓋校正)
	PrevPrice           float64 // 昨收價
	Volume              int     // 今日總成交量 (由 Yahoo 日K最新資料覆蓋校正)

	// 各指標數值、連續天數與買點狀態
	RSI                 float64 // 14日相對強弱指標 (0~100)
	RSIDays             int     // RSI 連續處於相對低檔/超賣天數
	IsRSIBuyPoint       bool    // 是否符合 RSI 買點 (價格低於 5 日均價)

	KD                  float64 // 9日隨機指標 K值 (0~100)
	KDDays              int     // KD 連續維持多頭排列 (K >= D) 天數
	IsKDBuyPoint        bool    // 是否符合 KD 買點 (價格高於 5 日均價)

	MACD                float64 // MACD DIF 指標值 (EMA12 - EMA26)
	MACDDays            int     // MACD 連續多頭紅柱 (DIF >= Signal) 天數
	IsMACDBuyPoint      bool    // 是否符合 MACD 買點 (價格高於 20 日均價)

	SMA                 float64 // 均線多頭排列強度 (5MA 對 20MA 之乖離率 %)
	SMADays             int     // 5MA 連續大於 20MA 之多頭排列天數
	IsSMABuyPoint       bool    // 是否符合 SMA 買點 (價格高於 20 日均價)

	Momentum            float64 // 10日動能漲幅 (%)
	MomentumDays        int     // 10日動能連續為正之天數
	IsMomentumBuyPoint  bool    // 是否符合動能買點 (價格高於 10 日均價)

	// 🎯 Volume Ratio 欄位 (與其他技術指標命名風格一致)
	VolumeRatio         float64 // 10日均量比值 (今日成交量 / 前10日均量基準)
	VolumeRatioDays     int     // 10日均量比值連續大於 1.5 的天數 (帶量突破發動)
	IsVolumeRatioBuyPoint bool  // 是否符合成交量均量比買點 (10日均量比值 > 1.5 帶量突破發動)

	Bollinger           float64 // 布林通道下軌乖離率 (負值代表跌破下軌)
	BollingerDays       int     // 連續跌破布林通道下軌天數
	IsBollingerBuyPoint bool    // 是否符合布林買點 (價格低於布林下軌)

	MA5                 float64 // 最近 5 日收盤均價
	MA10                float64 // 最近 10 日收盤均價
	MA20                float64 // 最近 20 日收盤均價
	BollingerDn         float64 // 最新布林下軌價格
	InstitutionalNetBuy int64   // 三大法人合計買賣超股數 (正為買超，負為賣超)
	CompanyInfo         string  // 公司資訊 (備用欄位)
}

// TWSET86Response 定義證交所官方三大法人日報 API 回傳之 JSON 結構
type TWSET86Response struct {
	Stat   string     `json:"stat"`
	Date   string     `json:"date"`
	Title  string     `json:"title"`
	Fields []string   `json:"fields"`
	Data   [][]string `json:"data"`
}

// =====================================================================
// 2. 核心爬蟲與單日計算邏輯 (TWSE OpenAPI & 官網)
// =====================================================================

// fetchStockData 透過 TWSE OpenAPI 抓取全市場股票名單與基礎量價
func fetchStockData() ([]StockData, error) {
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

		change, _ := strconv.ParseFloat(changeStr, 64)
		prevPrice := price
		if price > 0 {
			prevPrice = price - change
		}

		tempStocks = append(tempStocks, StockData{
			StockID:               stockID,
			StockName:             stockName,
			Price:                 price,
			PrevPrice:             prevPrice,
			Volume:                volume,
			RSI:                   -1.0,
			RSIDays:               0,
			IsRSIBuyPoint:         false,
			KD:                    -1.0,
			KDDays:                0,
			IsKDBuyPoint:          false,
			MACD:                  -9999.0,
			MACDDays:              0,
			IsMACDBuyPoint:        false,
			SMA:                   -9999.0,
			SMADays:               0,
			IsSMABuyPoint:         false,
			Momentum:              -9999.0,
			MomentumDays:          0,
			IsMomentumBuyPoint:    false,
			VolumeRatio:           -1.0,
			VolumeRatioDays:       0,
			IsVolumeRatioBuyPoint: false,
			Bollinger:             9999.0,
			BollingerDays:         0,
			IsBollingerBuyPoint:   false,
			MA5:                   0.0,
			MA10:                  0.0,
			MA20:                  0.0,
			BollingerDn:           0.0,
			InstitutionalNetBuy:   0,
		})
	}

	var stocks []StockData
	for _, stock := range tempStocks {
		if stock.Price <= 0 {
			continue // 排除暫停交易或價格無效者
		}
		stocks = append(stocks, stock)
	}

	return stocks, nil
}

// fetchInstitutionalData 抓取證交所三大法人買賣超 (具備盤中與假日自動回溯功能)
func fetchInstitutionalData() map[string]int64 {
	instMap := make(map[string]int64)
	client := &http.Client{Timeout: 15 * time.Second}
	now := time.Now()

	for daysBack := 0; daysBack < 5; daysBack++ {
		targetDate := now.AddDate(0, 0, -daysBack).Format("20060102")
		url := fmt.Sprintf("https://www.twse.com.tw/rwd/zh/fund/T86?date=%s&selectType=ALLBUT0999&response=json", targetDate)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
		req.Header.Set("Referer", "https://www.twse.com.tw/zh/trading/foreign/t86.html")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || len(bodyBytes) == 0 {
			continue
		}

		trimmed := strings.TrimSpace(string(bodyBytes))
		if strings.HasPrefix(trimmed, "<") {
			continue
		}

		var tResp TWSET86Response
		if err := json.Unmarshal(bodyBytes, &tResp); err != nil {
			continue
		}

		if tResp.Stat != "OK" || len(tResp.Data) == 0 {
			continue
		}

		colIdx := -1
		for idx, field := range tResp.Fields {
			if strings.Contains(field, "三大法人買賣超股數") || strings.Contains(field, "三大法人合計買賣超") {
				colIdx = idx
				break
			}
		}
		if colIdx == -1 {
			colIdx = len(tResp.Fields) - 1
		}

		for _, row := range tResp.Data {
			if len(row) <= colIdx {
				continue
			}
			code := strings.TrimSpace(row[0])
			if code == "" {
				continue
			}
			cleaned := strings.ReplaceAll(strings.ReplaceAll(row[colIdx], ",", ""), " ", "")
			if val, err := strconv.ParseInt(cleaned, 10, 64); err == nil {
				instMap[code] = val
			}
		}

		fmt.Printf("🏢 成功抓取三大法人買賣超資料 (最新日期: %s): %d 檔\n", targetDate, len(instMap))
		return instMap
	}

	fmt.Println("⚠️ 近期無三大法人日報資料")
	return instMap
}

// =====================================================================
// 3. Yahoo Finance 歷史技術指標深度計算邏輯
// =====================================================================

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

// scanBollingerBands 針對成交量前 500 大熱門股，呼叫 Yahoo API 計算所有真實歷史技術指標
func scanBollingerBands(stocks []StockData) {
	fmt.Println("📊 啟動歷史指標深度掃描：開始計算市場前 500 大個股之真實技術指標與專屬買點判定...")

	sortedByVol := make([]StockData, len(stocks))
	copy(sortedByVol, stocks)
	sort.Slice(sortedByVol, func(i, j int) bool {
		return sortedByVol[i].Volume > sortedByVol[j].Volume
	})

	topCount := 500
	if len(sortedByVol) < 500 {
		topCount = len(sortedByVol)
	}
	
	countCalculated := 0
	client := &http.Client{Timeout: 10 * time.Second}

	for i := 0; i < topCount; i++ {
		sid := sortedByVol[i].StockID
		url := fmt.Sprintf("https://query2.finance.yahoo.com/v8/finance/chart/%s.TW?range=3mo&interval=1d", sid)
		
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
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

			// 🎯 價格與成交量同步過濾：確保天數與時間點 100% 精準對齊
			var validPrices []float64
			var validVols []float64
			minLen := len(closes)
			if len(vols) < minLen {
				minLen = len(vols)
			}
			for k := 0; k < minLen; k++ {
				if closes[k] > 0 && vols[k] > 0 {
					validPrices = append(validPrices, closes[k])
					validVols = append(validVols, vols[k])
				}
			}
			
			if len(validPrices) >= 20 {
				for j := range stocks {
					if stocks[j].StockID == sid {
						latestP := validPrices[len(validPrices)-1]
						stocks[j].Price = latestP

						// 計算 5MA, 10MA, 20MA
						ma5 := calculateSMAFromPrices(validPrices, 5)
						ma10 := calculateSMAFromPrices(validPrices, 10)
						ma20 := calculateSMAFromPrices(validPrices, 20)
						stocks[j].MA5 = ma5
						stocks[j].MA10 = ma10
						stocks[j].MA20 = ma20

						// 1. 布林通道 (20MA, 2SD)
						pricesForBB := validPrices
						if len(pricesForBB) > 20 {
							pricesForBB = pricesForBB[len(pricesForBB)-20:]
						}
						_, _, dn := calculateBollinger(pricesForBB)
						stocks[j].BollingerDn = dn
						if dn > 0 {
							stocks[j].Bollinger = ((latestP - dn) / dn) * 100.0
							
							// 統計連續跌破布林下軌天數
							bbDays := 0
							for t := len(validPrices) - 1; t >= 19; t-- {
								w := validPrices[t-19 : t+1]
								_, _, wDn := calculateBollinger(w)
								if validPrices[t] < wDn {
									bbDays++
								} else {
									break
								}
							}
							stocks[j].BollingerDays = bbDays
							// 買點價格條件：價格低於布林下軌
							if latestP < dn {
								stocks[j].IsBollingerBuyPoint = true
							}
						}

						// 2. 🎯 真實 14 日 RSI
						rsiSeries := calculateAllRSI(validPrices, 14)
						latestRSI := rsiSeries[len(rsiSeries)-1]
						stocks[j].RSI = latestRSI
						
						rsiDays := 0
						rsiThreshold := 35.0
						if latestRSI > 35.0 {
							rsiThreshold = 45.0
						}
						for t := len(rsiSeries) - 1; t >= 14; t-- {
							if rsiSeries[t] <= rsiThreshold {
								rsiDays++
							} else {
								break
							}
						}
						if rsiDays == 0 {
							rsiDays = 1
						}
						stocks[j].RSIDays = rsiDays
						// 買點價格條件：價格低於 5 日均價 (負乖離超跌抄底)
						if latestP < ma5 {
							stocks[j].IsRSIBuyPoint = true
						}

						// 3. 🎯 真實 9 日 KD
						kSeries, dSeries := calculateAllKD(validPrices, 9)
						latestK := kSeries[len(kSeries)-1]
						stocks[j].KD = latestK
						
						kdDays := 0
						for t := len(kSeries) - 1; t >= 8; t-- {
							if kSeries[t] >= dSeries[t] {
								kdDays++
							} else {
								break
							}
						}
						if kdDays == 0 && latestK >= 50 {
							kdDays = 1
						}
						stocks[j].KDDays = kdDays
						// 買點價格條件：價格高於 5 日均價 (短多強勢站上攻擊線)
						if latestP > ma5 {
							stocks[j].IsKDBuyPoint = true
						}

						// 4. 🎯 真實 MACD (12, 26, 9)
						difSeries, sigSeries := calculateAllMACD(validPrices, 12, 26, 9)
						latestDIF := difSeries[len(difSeries)-1]
						stocks[j].MACD = latestDIF
						
						macdDays := 0
						for t := len(difSeries) - 1; t >= 0; t-- {
							if difSeries[t] >= sigSeries[t] {
								macdDays++
							} else {
								break
							}
						}
						if macdDays == 0 && latestDIF > 0 {
							macdDays = 1
						}
						stocks[j].MACDDays = macdDays
						// 買點價格條件：價格高於 20 日均價 (波段多頭站穩月線)
						if latestP > ma20 {
							stocks[j].IsMACDBuyPoint = true
						}

						// 5. 🎯 均線多頭強度：5MA 站上 20MA
						if ma20 > 0 {
							stocks[j].SMA = ((ma5 - ma20) / ma20) * 100.0
						}
						smaDays := 0
						for t := len(validPrices) - 1; t >= 19; t-- {
							m5 := calculateSMAFromPrices(validPrices[:t+1], 5)
							m20 := calculateSMAFromPrices(validPrices[:t+1], 20)
							if m5 > m20 {
								smaDays++
							} else {
								break
							}
						}
						stocks[j].SMADays = smaDays
						// 買點價格條件：價格高於 20 日均價 (守穩月線支撐)
						if latestP > ma20 {
							stocks[j].IsSMABuyPoint = true
						}

						// 6. 🎯 10 日真實價格動能 (%)
						stocks[j].Momentum = calculateRealMomentum(validPrices, 10)
						momDays := 0
						for t := len(validPrices) - 1; t >= 10; t-- {
							if validPrices[t] > validPrices[t-10] {
								momDays++
							} else {
								break
							}
						}
						stocks[j].MomentumDays = momDays
						// 買點價格條件：價格高於 10 日均價
						if latestP > ma10 {
							stocks[j].IsMomentumBuyPoint = true
						}

						// 7. 🎯 Volume Ratio 計算與買點判定 (與 Backstrategy 邏輯完全一致)
						if len(validVols) >= 11 {
							vrSeries := calculateVolumeRatio(validVols, 10)
							latestVR := vrSeries[len(vrSeries)-1]
							stocks[j].VolumeRatio = latestVR

							// 統計連續 10 日均量比 > 1.5 的天數 (帶量突破發動持續天數)
							vrDays := 0
							for t := len(vrSeries) - 1; t >= 10; t-- {
								if vrSeries[t] > 1.5 {
									vrDays++
								} else {
									break
								}
							}
							stocks[j].VolumeRatioDays = vrDays

							// 買點條件：10 日均量比值 > 1.5 (帶量突破發動)
							if latestVR > 1.5 {
								stocks[j].IsVolumeRatioBuyPoint = true
							} else {
								stocks[j].IsVolumeRatioBuyPoint = false
							}

							stocks[j].Volume = int(validVols[len(validVols)-1])
						}

						countCalculated++
						break
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	
	fmt.Printf("✅ 分析完成！成功深度運算各項技術指標: %d 檔個股。\n", countCalculated)
}

// calculateVolumeRatio 計算 Volume Ratio（10日均量比值：當日成交量 / 過去10日均量基準，不含當日）
func calculateVolumeRatio(volumes []float64, period int) []float64 {
	volRatio := make([]float64, len(volumes))
	for i := period; i < len(volumes); i++ {
		sum := 0.0
		// 計算過去 10 個交易日的成交量總和（不含當日）
		for j := i - period; j < i; j++ {
			sum += volumes[j]
		}
		avgVolume := sum / float64(period)
		if avgVolume > 0 {
			volRatio[i] = volumes[i] / avgVolume
		} else {
			volRatio[i] = 0.0
		}
	}
	return volRatio
}

// calculateBollinger 依據收盤價序列計算布林通道 (中軌、上軌、下軌)
func calculateBollinger(prices []float64) (mb, up, dn float64) {
	n := float64(len(prices))
	if n == 0 {
		return 0, 0, 0
	}
	var sum float64
	for _, p := range prices {
		sum += p
	}
	mb = sum / n

	var variance float64
	for _, p := range prices {
		variance += math.Pow(p-mb, 2)
	}
	sd := math.Sqrt(variance / n)

	up = mb + (2 * sd)
	dn = mb - (2 * sd)
	return mb, up, dn
}

// calculateAllRSI 計算整段時間序列的 Wilder's 14 日 RSI
func calculateAllRSI(prices []float64, period int) []float64 {
	n := len(prices)
	rsis := make([]float64, n)
	for i := range rsis {
		rsis[i] = 50.0
	}
	if n <= period {
		return rsis
	}

	gain, loss := 0.0, 0.0
	for i := 1; i <= period; i++ {
		chg := prices[i] - prices[i-1]
		if chg > 0 {
			gain += chg
		} else {
			loss -= chg
		}
	}
	avgGain := gain / float64(period)
	avgLoss := loss / float64(period)

	if avgLoss == 0 {
		rsis[period] = 100.0
	} else {
		rsis[period] = 100.0 - (100.0 / (1.0 + avgGain/avgLoss))
	}

	for i := period + 1; i < n; i++ {
		chg := prices[i] - prices[i-1]
		g := 0.0
		l := 0.0
		if chg > 0 {
			g = chg
		} else {
			l = -chg
		}
		avgGain = (avgGain*float64(period-1) + g) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + l) / float64(period)

		if avgLoss == 0 {
			rsis[i] = 100.0
		} else {
			rs := avgGain / avgLoss
			rsis[i] = 100.0 - (100.0 / (1.0 + rs))
		}
	}
	return rsis
}

// calculateAllKD 計算整段時間序列的 9 日 K 與 D
func calculateAllKD(prices []float64, period int) ([]float64, []float64) {
	n := len(prices)
	ks := make([]float64, n)
	ds := make([]float64, n)
	for i := range ks {
		ks[i] = 50.0
		ds[i] = 50.0
	}
	if n < period {
		return ks, ds
	}

	k, d := 50.0, 50.0
	for i := period - 1; i < n; i++ {
		window := prices[i-period+1 : i+1]
		low := window[0]
		high := window[0]
		for _, p := range window {
			if p < low {
				low = p
			}
			if p > high {
				high = p
			}
		}

		rsv := 50.0
		if high != low {
			rsv = (prices[i] - low) / (high - low) * 100.0
		}
		k = (2.0*k + rsv) / 3.0
		d = (2.0*d + k) / 3.0
		ks[i] = k
		ds[i] = d
	}
	return ks, ds
}

// calculateAllMACD 計算整段時間序列的 MACD DIF 與 Signal
func calculateAllMACD(prices []float64, shortP, longP, sigP int) ([]float64, []float64) {
	n := len(prices)
	difs := make([]float64, n)
	sigs := make([]float64, n)
	if n < longP {
		return difs, sigs
	}

	kShort := 2.0 / float64(shortP+1)
	kLong := 2.0 / float64(longP+1)
	kSig := 2.0 / float64(sigP+1)

	emaShort := prices[0]
	emaLong := prices[0]
	var rawDIFs []float64

	for _, p := range prices {
		emaShort = p*kShort + emaShort*(1.0-kShort)
		emaLong = p*kLong + emaLong*(1.0-kLong)
		rawDIFs = append(rawDIFs, emaShort-emaLong)
	}

	signal := rawDIFs[0]
	for i, d := range rawDIFs {
		signal = d*kSig + signal*(1.0-kSig)
		difs[i] = d
		sigs[i] = signal
	}
	return difs, sigs
}

// calculateSMAFromPrices 計算 N 日收盤均價
func calculateSMAFromPrices(prices []float64, period int) float64 {
	if len(prices) < period {
		return 0.0
	}
	window := prices[len(prices)-period:]
	sum := 0.0
	for _, p := range window {
		sum += p
	}
	return sum / float64(period)
}

// calculateRealMomentum 10 日動能漲跌幅 (%)
func calculateRealMomentum(prices []float64, period int) float64 {
	if len(prices) <= period {
		return 0.0
	}
	n := len(prices)
	prev := prices[n-1-period]
	current := prices[n-1]
	if prev > 0 {
		return ((current - prev) / prev) * 100.0
	}
	return 0.0
}

// =====================================================================
// 4. 排序與匯出邏輯 (買點優先排序)
// =====================================================================

// getTop10 依指定指標對股票進行過濾與排名，回傳前 10 名
func getTop10(stocks []StockData, indicator string) []StockData {
	var candidates []StockData
	for _, s := range stocks {
		if indicator == "RSI" && s.RSI < 0 {
			continue
		}
		if indicator == "KD" && s.KD < 0 {
			continue
		}
		if indicator == "MACD" && s.MACD < -9000 {
			continue
		}
		if indicator == "SMA" && s.SMA < -9000 {
			continue
		}
		if indicator == "Momentum" && s.Momentum < -9000 {
			continue
		}
		if (indicator == "Volume Ratio" || indicator == "成交量均量比策略（Volume Ratio）" || indicator == "ChipRatio") && s.VolumeRatio < 0 {
			continue
		}
		if indicator == "Bollinger" && s.Bollinger > 9000 {
			continue
		}
		candidates = append(candidates, s)
	}

	sort.Slice(candidates, func(i, j int) bool {
		switch indicator {
		case "RSI":
			// 🎯 買點優先：價格低於 5 日均價者優先排前
			if candidates[i].IsRSIBuyPoint != candidates[j].IsRSIBuyPoint {
				return candidates[i].IsRSIBuyPoint
			}
			return candidates[i].RSI < candidates[j].RSI
		case "KD":
			// 🎯 買點優先：價格高於 5 日均價者優先排前
			if candidates[i].IsKDBuyPoint != candidates[j].IsKDBuyPoint {
				return candidates[i].IsKDBuyPoint
			}
			return candidates[i].KD > candidates[j].KD
		case "MACD":
			// 🎯 買點優先：價格高於 20 日均價者優先排前
			if candidates[i].IsMACDBuyPoint != candidates[j].IsMACDBuyPoint {
				return candidates[i].IsMACDBuyPoint
			}
			return candidates[i].MACD > candidates[j].MACD
		case "SMA":
			// 🎯 買點優先：價格高於 20 日均價者優先排前
			if candidates[i].IsSMABuyPoint != candidates[j].IsSMABuyPoint {
				return candidates[i].IsSMABuyPoint
			}
			return candidates[i].SMA > candidates[j].SMA
		case "Momentum":
			// 🎯 買點優先：價格高於 10 日均價者優先排前
			if candidates[i].IsMomentumBuyPoint != candidates[j].IsMomentumBuyPoint {
				return candidates[i].IsMomentumBuyPoint
			}
			return candidates[i].Momentum > candidates[j].Momentum
		case "Volume Ratio", "成交量均量比策略（Volume Ratio）", "ChipRatio":
			// 🎯 買點優先：10 日均量比值 > 1.5 (帶量突破發動) 優先排前，再依均量比值降冪排序
			if candidates[i].IsVolumeRatioBuyPoint != candidates[j].IsVolumeRatioBuyPoint {
				return candidates[i].IsVolumeRatioBuyPoint
			}
			return candidates[i].VolumeRatio > candidates[j].VolumeRatio
		case "Bollinger":
			// 🎯 買點優先：價格跌破布林下軌者優先排前
			if candidates[i].IsBollingerBuyPoint != candidates[j].IsBollingerBuyPoint {
				return candidates[i].IsBollingerBuyPoint
			}
			return candidates[i].Bollinger < candidates[j].Bollinger
		}
		return false
	})

	if len(candidates) > 10 {
		return candidates[:10]
	}
	return candidates
}

// exportToCSV 將 7 大指標計算結果依格式輸出成 CSV 檔案 (第 8 欄為資料日期)
func exportToCSV(fileName string, allTop10 map[string][]StockData) error {
	file, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("❌ 無法建立 CSV: %v", err)
	}
	defer file.Close()

	file.WriteString("\xEF\xBB\xBF") // 寫入 UTF-8 BOM
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 1. 標題列
	writer.Write([]string{"技術指標", "股票代號", "名稱", "價格", "成交量", "指標值", "說明", "資料日期"})
	order := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "Volume Ratio", "Bollinger"}

	// 2. 取得今日產出檔案的確切日期 (YYYY-MM-DD)
	todayStr := time.Now().Format("2006-01-02")

	for _, indicator := range order {
		stocks, ok := allTop10[indicator]
		if !ok || len(stocks) == 0 {
			writer.Write([]string{indicator, "-", "-", "-", "-", "-", "今日無符合條件個股", todayStr})
			continue
		}

		for _, stock := range stocks {
			var valueStr string
			var desc string

			switch indicator {
			case "RSI":
				valueStr = fmt.Sprintf("%.2f", stock.RSI)
				if stock.IsRSIBuyPoint {
					desc = fmt.Sprintf("RSI 處於相對低檔，目前指標數值：%.2f (已連續符合 %d 日)，目前價格低於 5 日均價:此為近期買點", stock.RSI, stock.RSIDays)
				} else {
					desc = fmt.Sprintf("RSI 處於相對低檔，目前指標數值：%.2f (已連續符合 %d 日)，目前價格未低於 5 日均價:非近期買點", stock.RSI, stock.RSIDays)
				}
			case "KD":
				valueStr = fmt.Sprintf("%.2f", stock.KD)
				if stock.IsKDBuyPoint {
					desc = fmt.Sprintf("KD 呈現多頭向上攻擊，目前指標數值：%.2f (已連續符合 %d 日)，目前價格高於 5 日均價:此為近期買點", stock.KD, stock.KDDays)
				} else {
					desc = fmt.Sprintf("KD 呈現多頭向上攻擊，目前指標數值：%.2f (已連續符合 %d 日)，目前價格未高於 5 日均價:非近期買點", stock.KD, stock.KDDays)
				}
			case "MACD":
				valueStr = fmt.Sprintf("%.2f", stock.MACD)
				if stock.IsMACDBuyPoint {
					desc = fmt.Sprintf("MACD 處於多頭上升波段，目前指標數值：%.2f (已連續符合 %d 日)，目前價格高於 20 日均價:此為近期買點", stock.MACD, stock.MACDDays)
				} else {
					desc = fmt.Sprintf("MACD 處於多頭上升波段，目前指標數值：%.2f (已連續符合 %d 日)，目前價格未高於 20 日均價:非近期買點", stock.MACD, stock.MACDDays)
				}
			case "SMA":
				valueStr = fmt.Sprintf("%.2f%%", stock.SMA)
				if stock.IsSMABuyPoint {
					desc = fmt.Sprintf("5MA 站上 20MA 多頭排列 (乖離率 %+.2f%%)，目前指標數值：%+.2f%% (已連續符合 %d 日)，目前價格高於 20 日均價:此為近期買點", stock.SMA, stock.SMA, stock.SMADays)
				} else {
					desc = fmt.Sprintf("5MA 站上 20MA 多頭排列 (乖離率 %+.2f%%)，目前指標數值：%+.2f%% (已連續符合 %d 日)，目前價格未高於 20 日均價:非近期買點", stock.SMA, stock.SMA, stock.SMADays)
				}
			case "Momentum":
				valueStr = fmt.Sprintf("%+.2f%%", stock.Momentum)
				if stock.IsMomentumBuyPoint {
					desc = fmt.Sprintf("近 10 日動能強勁 (漲幅 %+.2f%%)，目前指標數值：%+.2f%% (已連續符合 %d 日)，目前價格高於 10 日均價:此為近期買點", stock.Momentum, stock.Momentum, stock.MomentumDays)
				} else {
					desc = fmt.Sprintf("近 10 日動能向上 (漲幅 %+.2f%%)，目前指標數值：%+.2f%% (已連續符合 %d 日)，目前價格未高於 10 日均價:非近期買點", stock.Momentum, stock.Momentum, stock.MomentumDays)
				}
			case "Volume Ratio", "成交量均量比策略（Volume Ratio）", "ChipRatio":
				valueStr = fmt.Sprintf("%.2f", stock.VolumeRatio)
				if stock.IsVolumeRatioBuyPoint {
					desc = fmt.Sprintf("成交量帶量突破 (10 日均量比值 > 1.5，跌破 10MA 或爆量長黑平倉)，目前指標數值：%.2f (已連續符合 %d 日)，10 日均量比值大於 1.5:此為近期買點(帶量突破發動)", stock.VolumeRatio, stock.VolumeRatioDays)
				} else {
					desc = fmt.Sprintf("成交量均量比值未達發動標準 (10 日均量比值需 > 1.5)，目前指標數值：%.2f (已連續符合 %d 日)，10 日均量比值未大於 1.5:非近期買點", stock.VolumeRatio, stock.VolumeRatioDays)
				}
			case "Bollinger":
				if stock.Bollinger > 5.0 {
					continue
				}
				valueStr = fmt.Sprintf("%.2f%%", stock.Bollinger)
				if stock.IsBollingerBuyPoint {
					desc = fmt.Sprintf("💥 跌破布林下軌 (乖離率 %.2f%%)，目前指標數值：%.2f%% (已連續符合 %d 日)，目前價格低於布林下軌:此為近期買點", stock.Bollinger, stock.Bollinger, stock.BollingerDays)
				} else {
					desc = fmt.Sprintf("貼近布林下軌 (乖離率 %.2f%%)，目前指標數值：%.2f%%，目前價格未低於布林下軌:非近期買點", stock.Bollinger, stock.Bollinger)
				}
			}

			// 每一列固定帶入 todayStr 作為確切日期標記
			writer.Write([]string{
				indicator,
				stock.StockID,
				stock.StockName,
				fmt.Sprintf("%.2f", stock.Price), 
				strconv.Itoa(stock.Volume),       
				valueStr,
				desc,
				todayStr,
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

	// 步驟 1-1：取得全市場三大法人最新買賣超資料並配對
	instMap := fetchInstitutionalData()
	for i := range stocks {
		if netBuy, ok := instMap[stocks[i].StockID]; ok {
			stocks[i].InstitutionalNetBuy = netBuy
		}
	}

	// 步驟 2：針對前 500 大個股進行歷史 K 線深度掃描 (全面運算各指標、連續天數與買點判定)
	scanBollingerBands(stocks)

	// 步驟 3：依各真實指標排序取 Top 10 (各指標買點優先排序)
	indicators := []string{"RSI", "KD", "MACD", "SMA", "Momentum", "Volume Ratio", "Bollinger"}
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
	
	fmt.Println("🎉 全市場真實技術指標掃描任務完成！")
}