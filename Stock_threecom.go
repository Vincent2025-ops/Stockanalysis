package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
	"io/ioutil"
	"strconv"
	"regexp"
)

// 取得最近的交易日
func getLatestTradingDate() string {
	now := time.Now()
	weekday := now.Weekday()

	if weekday == time.Sunday {
		now = now.AddDate(0, 0, -2)
	} else if weekday == time.Saturday {
		now = now.AddDate(0, 0, -1)
	}

	return now.Format("20060102")
}

// 回退至最近有資料的交易日
func findValidTradingDate() string {
	date := getLatestTradingDate()
	for i := 0; i < 10; i++ { // 最多回退 10 天
		fmt.Printf("🔍 嘗試查詢日期: %s\n", date)
		if hasData(date) {
			return date
		}
		date = previousDate(date) // 回退一天
	}
	fmt.Println("⚠️ 無法找到最近 10 天內的有效交易資料")
	return getLatestTradingDate()
}

// 確認該日期是否有法人交易資料
func hasData(date string) bool {
	url := fmt.Sprintf("https://www.twse.com.tw/rwd/zh/fund/T86?date=%s&selectType=ALL&response=csv", date)

	client := &http.Client{}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://www.twse.com.tw/")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "<!DOCTYPE html>") {
			return false
		}
		if strings.Contains(line, ",") { // 有逗號表示是 CSV
			return true
		}
	}
	return false
}

// 取得前一天的日期（跳過週末）
func previousDate(date string) string {
	t, _ := time.Parse("20060102", date)
	t = t.AddDate(0, 0, -1) // 回退一天

	if t.Weekday() == time.Saturday {
		t = t.AddDate(0, 0, -1)
	}
	if t.Weekday() == time.Sunday {
		t = t.AddDate(0, 0, -2)
	}
	return t.Format("20060102")
}

// 下載並轉換 CSV 檔案
func fetchCSV(date string) ([]string, error) {
	url := fmt.Sprintf("https://www.twse.com.tw/rwd/zh/fund/T86?date=%s&selectType=ALL&response=csv", date)

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("建立 HTTP 請求失敗: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://www.twse.com.tw/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下載 CSV 失敗: %v", err)
	}
	defer resp.Body.Close()

	// **Big5 轉 UTF-8**
	utf8Reader := transform.NewReader(resp.Body, traditionalchinese.Big5.NewDecoder())
	utf8Data, err := ioutil.ReadAll(utf8Reader)
	if err != nil {
		return nil, fmt.Errorf("轉換 Big5 為 UTF-8 失敗: %v", err)
	}

	// **解析 UTF-8 內容**
	lines := strings.Split(string(utf8Data), "\n")

	if len(lines) == 0 || strings.Contains(lines[0], "<!DOCTYPE html>") {
		return nil, fmt.Errorf("⚠️ 當日 (%s) 無法人交易資料", date)
	}

	return lines, nil
}

// 解析 CSV 並查詢股票
func parseCSV(lines []string, stockID string, date string) (map[string]string, error) {
	csvData := strings.Join(lines, "\n")
	reader := csv.NewReader(strings.NewReader(csvData))
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析 CSV 失敗: %v", err)
	}

	for _, record := range records {
		if len(record) > 1 {
			csvStockID := strings.TrimSpace(record[0])
			if csvStockID == stockID {
				data := make(map[string]string)
				data["證券代號"] = csvStockID
				data["證券名稱"] = record[1]

				// 讀取欄位，預設為 0 避免空值
				data["外陸資買進股數"] = getValidNumber(record, 2)
				data["外陸資賣出股數"] = getValidNumber(record, 3)
				data["外陸資買賣超股數"] = getValidNumber(record, 4)
				data["投信買進股數"] = getValidNumber(record, 8)
				data["投信賣出股數"] = getValidNumber(record, 9)
				data["投信買賣超股數"] = getValidNumber(record, 10)
				data["自營商買進股數"] = getValidNumber(record, 11)
				data["自營商賣出股數"] = getValidNumber(record, 12)
				data["自營商買賣超股數"] = getValidNumber(record, 13)
				data["外資自營商買進股數"] = getValidNumber(record, 5)
				data["外資自營商賣出股數"] = getValidNumber(record, 6)
				data["外資自營商買賣超股數"] = getValidNumber(record, 7)

				// 計算三大法人買賣超股數
				foreign, _ := strconv.Atoi(data["外陸資買賣超股數"])
				investment, _ := strconv.Atoi(data["投信買賣超股數"])
				dealer, _ := strconv.Atoi(data["自營商買賣超股數"])
				threeCorpTotal := foreign + investment + dealer
				data["三大法人買賣超股數"] = fmt.Sprintf("%d", threeCorpTotal)

				return data, nil
			}
		}
	}
	return nil, fmt.Errorf("⚠️ 股票代號 %s 在當日 (%s) 無交易資料", stockID, date)
}

// 確保數字欄位有值，避免出錯
func getValidNumber(record []string, index int) string {
	if len(record) > index {
		return strings.ReplaceAll(record[index], ",", "")
	}
	return "0"
}


// fetchGoodinfoData: 爬取 Goodinfo 的股票籌碼數據
func fetchGoodinfoData(stockID string) (map[string]string, error) {
	url := fmt.Sprintf("https://goodinfo.tw/tw/ShowK_Chart.asp?STOCK_ID=%s&CHT_CAT=YEAR", stockID)
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("建立 HTTP 請求失敗: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://goodinfo.tw/tw/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下載 Goodinfo 資料失敗: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("讀取 Goodinfo 回應失敗: %v", err)
	}
	content := string(body)

	// 正則表達式擷取關鍵數據
	data := make(map[string]string)

	data["主力買賣超"], _ = extractData(content, "主力買賣超\\s*</td><td[^>]*>(-?[\\d,]+)")

	data["散戶持股比例"], _ = extractData(content, "散戶持股比例\\s*</td><td[^>]*>([\\d.]+)%")

	data["大戶持股比例"], _ = extractData(content, "大戶持股比例\\s*</td><td[^>]*>([\\d.]+)%")

	data["換手率"], _ = extractData(content, "換手率\\s*</td><td[^>]*>([\\d.]+)%")

	return data, nil
}

// extractData: 使用正則表達式擷取數據
func extractData(content, pattern string) (string, error) {
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(content)
	if len(matches) > 1 {
		return matches[1], nil
	}
	return "N/A", fmt.Errorf("未找到匹配數據")
}

func printStockData(data map[string]string) {
	fmt.Println("✅ 查詢結果:")
	fmt.Printf("📌 證券代號: %s\n", data["證券代號"])
	fmt.Printf("🏢 證券名稱: %s\n", data["證券名稱"])
	fmt.Printf("💰 外陸資買進股數: %s\n", data["外陸資買進股數"])
	fmt.Printf("💰 外陸資賣出股數: %s\n", data["外陸資賣出股數"])
	fmt.Printf("📊 外陸資買賣超股數: %s\n", data["外陸資買賣超股數"])
	fmt.Printf("📈 投信買進股數: %s\n", data["投信買進股數"])
	fmt.Printf("📉 投信賣出股數: %s\n", data["投信賣出股數"])
	fmt.Printf("📊 投信買賣超股數: %s\n", data["投信買賣超股數"])
	fmt.Printf("🏦 自營商買進股數: %s\n", data["自營商買進股數"])
	fmt.Printf("🏦 自營商賣出股數: %s\n", data["自營商賣出股數"])
	fmt.Printf("📊 自營商買賣超股數: %s\n", data["自營商買賣超股數"])
	fmt.Printf("🌍 外資自營商買進股數: %s\n", data["外資自營商買進股數"])
	fmt.Printf("🌍 外資自營商賣出股數: %s\n", data["外資自營商賣出股數"])
	fmt.Printf("📊 外資自營商買賣超股數: %s\n", data["外資自營商買賣超股數"])
	fmt.Printf("📈 三大法人買賣超股數: %s\n", data["三大法人買賣超股數"])
}

// 主程式
func main() {
	date := findValidTradingDate()
	fmt.Printf("📅 使用最近有效交易日: %s\n", date)

	var stockID string
	fmt.Print("請輸入股票代號: ")
	fmt.Scanln(&stockID)

	lines, err := fetchCSV(date)
	if err != nil {
		fmt.Println("❌ 錯誤:", err)
		return
	}

	stockData, err := parseCSV(lines, stockID, date) // ✅ 傳入 date
	if err != nil {
		fmt.Println("⚠️", err)
		return
	}

	// 📌 呼叫統一格式輸出函數
	printStockData(stockData)
	
	//goodinfoData數值
	goodinfoData, err := fetchGoodinfoData(stockID)
	if err != nil {
		fmt.Println("❌ 錯誤:", err)
		return
	}
	//goodinfoData籌碼分析
	fmt.Println("📊 Goodinfo 籌碼分析:")
	fmt.Printf("🔹 主力買賣超: %s	", goodinfoData["主力買賣超"])
		fmt.Printf("🔹 散戶持股比例: %s	", goodinfoData["散戶持股比例"])
		fmt.Printf("🔹 大戶持股比例: %s	", goodinfoData["大戶持股比例"])
		fmt.Printf("🔹 換手率: %s	", goodinfoData["換手率"])
	
}
