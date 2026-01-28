/*已可取得三大法人買賣資訊，尚缺:
主力動向 → 用 前 5 大買賣家 或 大戶持股變化 分析（可從 Goodinfo、CMoney 爬取）。
散戶動向 → 看 融資融券變化、當沖比（TWSE、Goodinfo、CMoney 提供）。
籌碼集中度 → 依 大戶持股變化 計算（Goodinfo、CMoney 提供）。
大戶進出 → 透過 持股 400 張 / 1000 張以上的投資人變化 分析（Goodinfo、CMoney 提供）。
換手率 → 成交量 / 總股數，TWSE 可查詢成交量自行計算。
*/
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
	
}
