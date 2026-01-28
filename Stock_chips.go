//已可取得json籌碼面及指標計算，缺少法人進出
package main

import (
//	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"time"
//	"github.com/chromedp/chromedp"
	"math"
)

// TWSEChipData 結構體用於存放從 TWSE API 取得的籌碼數據
type TWSEChipData struct {
	StockCode         string `json:"股票代號"`
	StockName         string `json:"股票名稱"`
	FinBuy            string `json:"融資買進"`
	FinSell           string `json:"融資賣出"`
	FinCashRepay      string `json:"融資現金償還"`
	FinPrevBalance    string `json:"融資前日餘額"`
	FinCurrentBalance string `json:"融資今日餘額"`
	FinQuota          string `json:"融資限額"`
	SecBuy            string `json:"融券買進"`
	SecSell           string `json:"融券賣出"`
	SecStockRepay     string `json:"融券現券償還"`
	SecPrevBalance    string `json:"融券前日餘額"`
	SecCurrentBalance string `json:"融券今日餘額"`
	SecQuota          string `json:"融券限額"`
}

// fetchTWSEChipData 取得 TWSE API 指定日期的數據
func fetchTWSEChipData(date, stockCode string) (*TWSEChipData, error) {
	url := fmt.Sprintf("https://openapi.twse.com.tw/v1/exchangeReport/MI_MARGN?date=%s", date)
	fmt.Println("查詢 URL:", url)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("TWSE API 請求失敗: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("讀取 API 回應失敗: %v", err)
	}

	var stockDataList []TWSEChipData
	if err := json.Unmarshal(body, &stockDataList); err != nil {
		return nil, fmt.Errorf("JSON 解析失敗: %v", err)
	}

	for _, stock := range stockDataList {
		if stock.StockCode == stockCode {
			return &stock, nil
		}
	}
	return nil, fmt.Errorf("未找到股票代號 %s 的籌碼資料", stockCode)
}

// 軋空力道計算
type ShortSqueezeData struct {
	FiveDayAvgShortBalance float64 // 5日均融券餘額
	SecCurrentBalance    float64 // 當日融券餘額=融券今日餘額
}

// 取得 5 日均融券餘額
func fetchFiveDayAvgShortBalance(stockCode string) (float64, error) {
    url := fmt.Sprintf("https://openapi.twse.com.tw/v1/exchangeReport/MI_MARGN?stockNo=%s", stockCode)
    client := &http.Client{Timeout: 10 * time.Second}

    log.Println("🔍 正在從 TWSE API 獲取 5 日均融券餘額:", url)

    resp, err := client.Get(url)
    if err != nil {
        log.Println("❌ TWSE API 請求失敗:", err)
        return 0, fmt.Errorf("TWSE API 請求失敗")
    }
    defer resp.Body.Close()

    if resp.StatusCode != 200 {
        log.Println("❌ TWSE API 回應錯誤:", resp.Status)
        return 0, fmt.Errorf("API 回應錯誤: %s", resp.Status)
    }

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        log.Println("❌ 讀取 TWSE API 回應失敗:", err)
        return 0, fmt.Errorf("讀取 API 回應失敗")
    }

    var marginData []struct {
        StockNo  string `json:"StockNo"`
        ShortBal string `json:"融券今日餘額"`
    }

    if err := json.Unmarshal(body, &marginData); err != nil {
        log.Println("❌ JSON 解析失敗:", err)
        return 0, fmt.Errorf("解析 TWSE API 失敗")
    }

    if len(marginData) == 0 {
        log.Println("⚠️ 未獲取到有效的融券數據")
        return 0, fmt.Errorf("無法獲取融券數據")
    }

    shortBal, err := strconv.ParseFloat(marginData[0].ShortBal, 64)
    if err != nil {
        log.Println("❌ 融券餘額數據轉換失敗:", marginData[0].ShortBal)
        return 0, fmt.Errorf("數據轉換失敗")
    }

    return shortBal, nil
}


// parseStringToFloat 轉換數值型的字串為 float64
func parseStringToFloat(s string) float64 {
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return value
}

// 輔助函式：格式化輸出，避免 NaN 顯示
func formatOutput(value float64) string {
    if math.IsNaN(value) {
        return "無法計算"
    }
    return fmt.Sprintf("%.2f%%", value)
}

// 安全計算比率函式，避免 0 或 NaN，並回傳 float64
func safeDivide(numerator, denominator float64) float64 {
    if denominator == 0 {
        return math.NaN() // 回傳 NaN 以標記無法計算
    }
    return (numerator / denominator) * 100
}

func main() {
    var stockCode string
    fmt.Print("請輸入股票代號: ")
    fmt.Scanln(&stockCode)

    // 設定查詢日期
    date := time.Now().Format("20060102")

    chipData, err := fetchTWSEChipData(date, stockCode)
    if err != nil {
        log.Fatalf("❌ 無法取得 TWSE 籌碼數據: %v", err)
    }

    // 轉換數值類型
	finBuy := parseStringToFloat(chipData.FinBuy)
	finSell := parseStringToFloat(chipData.FinSell)
	finCashRepay := parseStringToFloat(chipData.FinCashRepay)
	secSell := parseStringToFloat(chipData.SecSell)
	secStockRepay := parseStringToFloat(chipData.SecStockRepay)
	secBuy := parseStringToFloat(chipData.SecBuy)
    finCurrentBalance := parseStringToFloat(chipData.FinCurrentBalance)
    secCurrentBalance := parseStringToFloat(chipData.SecCurrentBalance)
    finQuota := parseStringToFloat(chipData.FinQuota)
    secQuota := parseStringToFloat(chipData.SecQuota)
    finPrevBalance := parseStringToFloat(chipData.FinPrevBalance)
    secPrevBalance := parseStringToFloat(chipData.SecPrevBalance)
	
	// 取得 5 日均融券餘額
    fiveDayAvgShortBalance, err := fetchFiveDayAvgShortBalance(stockCode)
    if err != nil {
        log.Println("⚠️ 無法獲取 5 日均融券餘額，使用預設值 0")
        fiveDayAvgShortBalance = 0.0
    }

    // 計算指標
    var marginShortRatio float64 = math.NaN()
    var marginUsage float64 = math.NaN()
    var shortUsage float64 = math.NaN()
    var marginChangeRate float64 = math.NaN()
    var shortChangeRate float64 = math.NaN()
    var squeezeForce float64 = math.NaN()
    var shortSqueezeStrength float64 = math.NaN()
	
	if secCurrentBalance > 0 {
        marginShortRatio = finCurrentBalance / secCurrentBalance
    }
    if finQuota > 0 {
        marginUsage = safeDivide(finCurrentBalance, finQuota)
    }
    if secQuota > 0 {
        shortUsage = safeDivide(secCurrentBalance, secQuota)
    }
    if finPrevBalance > 0 {
        marginChangeRate = safeDivide(finCurrentBalance-finPrevBalance, finPrevBalance)
    }
    if secPrevBalance > 0 {
        shortChangeRate = safeDivide(secCurrentBalance-secPrevBalance, secPrevBalance)
    }
    if !math.IsNaN(marginChangeRate) && !math.IsNaN(shortChangeRate) {//squeezeForce 計算擠壓力道
        squeezeForce = marginChangeRate - shortChangeRate
    }
    if fiveDayAvgShortBalance > 0 {// 計算軋空力道
        shortSqueezeStrength = safeDivide(fiveDayAvgShortBalance-secCurrentBalance, fiveDayAvgShortBalance)
    }


    fmt.Println("\n==== 籌碼面資料 ====")
    fmt.Printf("股票代號: %s (%s)\n", chipData.StockCode, chipData.StockName)
    fmt.Println("==== 融資 ====")	
	fmt.Printf("融資買進: %.0f 張\n", finBuy)
	fmt.Printf("融資賣出: %.0f 張\n", finSell)
	fmt.Printf("融資現金償還: %.0f 張\n", finCashRepay)
	fmt.Printf("融資前日餘額: %.0f 張\n", finPrevBalance)
	fmt.Printf("融資今日餘額(所有投資者使用融資買進某一檔股票的總張數): %.0f 張\n", finCurrentBalance)
	fmt.Printf("融資使用率: %.2f%%\n", marginUsage)
	fmt.Printf("融資增減幅: %.2f%%\n", marginChangeRate)
	fmt.Printf("融資限額: %.0f 張\n", finQuota)
	
	fmt.Println("==== 融券 ====")
	fmt.Printf("融券買進: %.0f 張\n", secBuy)
	fmt.Printf("融券賣出: %.0f 張\n", secSell)
	fmt.Printf("融券現券償還: %.0f 張\n", secStockRepay)
	fmt.Printf("融券前日餘額: %.0f 張\n", secPrevBalance)
    fmt.Printf("融券今日餘額(所有投資者使用融券賣出（放空）某一檔股票的總張數): %.0f 張\n", secCurrentBalance)
	fmt.Printf("融券使用率: %.2f%%\n", shortUsage)
    fmt.Printf("融券增減幅: %.2f%%\n", shortChangeRate)
	fmt.Printf("融券限額: %.0f 張\n", secQuota)
	
	fmt.Println("==== 資券計算 ====")
    fmt.Printf("5 日均融券餘額: %.0f 張\n", fiveDayAvgShortBalance)
    fmt.Printf("融資融券比 (多空比): %.2f\n", marginShortRatio)  
    fmt.Printf("擠壓力道 (squeezeForce): %.2f\n", squeezeForce)
    fmt.Printf("軋空力道 (Short Squeeze Strength): %.2f%%\n", shortSqueezeStrength)

    fmt.Println("==== 趨勢評估 ====")
    if marginUsage > 80 {
        fmt.Println("⚠️ 融資使用率 > 80%，過度槓桿，市場可能過熱，容易回檔")
    } else if marginUsage > 50 {
        fmt.Println("📈 融資使用率 50~80%，市場活躍，多方占優勢")
    } else {
        fmt.Println("🔄 融資使用率 < 50%，市場風險較低，但也可能代表觀望氣氛濃厚")
    }

    if shortUsage > 80 {
        fmt.Println("⚠️ 融券使用率 > 80%，市場對個股看空情緒濃厚，容易出現『軋空』")
    } else if shortUsage > 50 {
        fmt.Println("📉 融券使用率 50~80%，市場偏空，但尚未進入極端情況")
    } else {
        fmt.Println("🔄 融券使用率 < 50%，市場看空力道不強，短期內股價可能較為平穩")
    }
	
	if marginShortRatio > 10 {
        fmt.Println("⚠️ 融資融券比 > 10，市場過度樂觀，多方主導，可能有回檔風險")
    } else if marginShortRatio > 5 {
        fmt.Println("📈 融資融券比 5~10，市場偏多，股價可能穩步上漲")
    } else if marginShortRatio > 2 {
        fmt.Println("🔄 融資融券比 2~5，市場趨於平衡")
    } else {
        fmt.Println("📉 融資融券比 < 2，市場偏空，可能有軋空機會")
    }
	
		// **新增擠壓力道 (squeezeForce) 評估**
    fmt.Println("==== 擠壓力道評估 ====")
    if squeezeForce > 10 {
        fmt.Println("🚀 >10，市場多方動能強，股價可能持續上漲，可能觸發軋空行情！**")
    } else if squeezeForce > 5 {
        fmt.Println("📈 5 ~ 10，市場多方動能增強，股價可能上漲")
    } else if squeezeForce > -5 {
        fmt.Println("🔄 -5 ~ 5，多空力量均衡，市場相對穩定")
    } else if squeezeForce > -10 {
        fmt.Println("📉 -10 ~ -5， 空方力量增強，股價可能承壓")
    } else {
        fmt.Println("⚠️ <-10，市場空方動能強，股價可能持續下跌，可能發生多殺多風險！**")
    }
	
	// **評估軋空力道**
    fmt.Println("==== 軋空力道評估 ====")
    if shortSqueezeStrength > 20 {
        fmt.Println("🚀 > 20，**有明顯軋空行情，股價可能快速拉升！**")
    } else if shortSqueezeStrength > 10 {
        fmt.Println("📈 > 10，**可能有部分軋空力道**")
    } else {
        fmt.Println("🔄 **空單回補不明顯**")
    }	
}
