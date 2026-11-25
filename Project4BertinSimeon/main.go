package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/xuri/excelize/v2"
)

type StateInfo struct {
	Code string
	Name string
}

var allStates = []StateInfo{
	{"AL", "Alabama"}, {"AK", "Alaska"}, {"AZ", "Arizona"}, {"AR", "Arkansas"},
	{"CA", "California"}, {"CO", "Colorado"}, {"CT", "Connecticut"}, {"DE", "Delaware"},
	{"FL", "Florida"}, {"GA", "Georgia"}, {"HI", "Hawaii"}, {"ID", "Idaho"},
	{"IL", "Illinois"}, {"IN", "Indiana"}, {"IA", "Iowa"}, {"KS", "Kansas"},
	{"KY", "Kentucky"}, {"LA", "Louisiana"}, {"ME", "Maine"}, {"MD", "Maryland"},
	{"MA", "Massachusetts"}, {"MI", "Michigan"}, {"MN", "Minnesota"}, {"MS", "Mississippi"},
	{"MO", "Missouri"}, {"MT", "Montana"}, {"NE", "Nebraska"}, {"NV", "Nevada"},
	{"NH", "New Hampshire"}, {"NJ", "New Jersey"}, {"NM", "New Mexico"}, {"NY", "New York"},
	{"NC", "North Carolina"}, {"ND", "North Dakota"}, {"OH", "Ohio"}, {"OK", "Oklahoma"},
	{"OR", "Oregon"}, {"PA", "Pennsylvania"}, {"RI", "Rhode Island"}, {"SC", "South Carolina"},
	{"SD", "South Dakota"}, {"TN", "Tennessee"}, {"TX", "Texas"}, {"UT", "Utah"},
	{"VT", "Vermont"}, {"VA", "Virginia"}, {"WA", "Washington"}, {"WV", "West Virginia"},
	{"WI", "Wisconsin"}, {"WY", "Wyoming"},
	{"DC", "District of Columbia"},
	{"PR", "Puerto Rico"},
	{"US", "Nationwide"},
}

func main() {
	dbPath := "economic_data.db"
	_ = os.Remove(dbPath)

	// 1. Validate required workbook presence before running
	requiredFiles := []string{
		"BLSsalaryDara2025.xlsx",
		"RentPer10SquareFt2026.xlsx",
		"homepriceindex.xlsx",
	}
	for _, f := range requiredFiles {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			log.Fatalf("Required workbook %q not found in current directory.", f)
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal("Error opening database:", err)
	}
	defer db.Close()

	// 2. Apply SQLite performance optimizations and enable foreign keys
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA temp_store = MEMORY;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			log.Fatalf("Error executing %s: %v", p, err)
		}
	}

	createTables(db)
	validStates := populateStates(db)

	salaryCount := loadSalaryData(db, "BLSsalaryDara2025.xlsx", validStates)
	rentCount := loadRentData(db, "RentPer10SquareFt2026.xlsx", validStates)
	hpiCount := loadHomePriceData(db, "homepriceindex.xlsx", validStates)

	fmt.Println("=== Database Load Complete ===")
	fmt.Printf("Database saved to: %s\n", dbPath)
	fmt.Printf("States inserted:      %d\n", len(allStates))
	fmt.Printf("Salary rows inserted: %d\n", salaryCount)
	fmt.Printf("Rent rows inserted:   %d\n", rentCount)
	fmt.Printf("HPI 2026 rows added:  %d\n", hpiCount)
}

func createTables(db *sql.DB) {
	schema := `
	CREATE TABLE IF NOT EXISTS states (
		state_code TEXT PRIMARY KEY,
		state_name TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS salaries (
		salary_id INTEGER PRIMARY KEY AUTOINCREMENT,
		state_code TEXT NOT NULL,
		area_title TEXT NOT NULL,
		occ_code TEXT NOT NULL,
		occ_title TEXT NOT NULL,
		o_group TEXT,
		tot_emp INTEGER,
		h_mean REAL,
		a_mean REAL,
		h_median REAL,
		a_median REAL,
		FOREIGN KEY (state_code) REFERENCES states(state_code)
	);

	CREATE TABLE IF NOT EXISTS rent_prices (
		rent_id INTEGER PRIMARY KEY AUTOINCREMENT,
		state_code TEXT NOT NULL,
		metro_name TEXT NOT NULL,
		period TEXT NOT NULL,
		rent_value REAL,
		preceding_period REAL,
		year_ago REAL,
		FOREIGN KEY (state_code) REFERENCES states(state_code)
	);

	CREATE TABLE IF NOT EXISTS home_prices (
		hpi_id INTEGER PRIMARY KEY AUTOINCREMENT,
		state_code TEXT NOT NULL,
		level TEXT NOT NULL,
		place_name TEXT NOT NULL,
		place_id TEXT NOT NULL,
		hpi_type TEXT,
		hpi_flavor TEXT,
		frequency TEXT,
		yr INTEGER NOT NULL,
		period INTEGER NOT NULL,
		index_nsa REAL,
		index_sa REAL,
		FOREIGN KEY (state_code) REFERENCES states(state_code)
	);

	-- Add indexes for performance as recommended
	CREATE INDEX IF NOT EXISTS idx_salary_state ON salaries(state_code);
	CREATE INDEX IF NOT EXISTS idx_rent_state ON rent_prices(state_code);
	CREATE INDEX IF NOT EXISTS idx_home_state ON home_prices(state_code);
	CREATE INDEX IF NOT EXISTS idx_home_year ON home_prices(yr);
	`

	if _, err := db.Exec(schema); err != nil {
		log.Fatal("Error creating tables and indexes:", err)
	}
}

func populateStates(db *sql.DB) map[string]bool {
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback() // Safe rollback if anything panics before commit

	stmt, err := tx.Prepare("INSERT OR IGNORE INTO states (state_code, state_name) VALUES (?, ?)")
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	valid := make(map[string]bool, len(allStates))
	for _, s := range allStates {
		if _, err := stmt.Exec(s.Code, s.Name); err != nil {
			log.Fatalf("Error inserting state %s: %v", s.Code, err)
		}
		valid[s.Code] = true
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	return valid
}

func loadSalaryData(db *sql.DB, filename string, validStates map[string]bool) int {
	f, err := excelize.OpenFile(filename)
	if err != nil {
		log.Fatalf("Failed to open %s: %v", filename, err)
	}
	defer f.Close()

	rows, err := f.GetRows("state_M2025_dl")
	if err != nil {
		log.Fatalf("Failed to read sheet state_M2025_dl: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO salaries (
			state_code, area_title, occ_code, occ_title, o_group,
			tot_emp, h_mean, a_mean, h_median, a_median
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	inserted := 0
	for i, row := range rows {
		if i == 0 || len(row) < 28 { // Ensure all required columns up to index 27 exist
			continue
		}
		areaTitle := strings.TrimSpace(row[1])
		stateCode := strings.ToUpper(strings.TrimSpace(row[3]))

		if !validStates[stateCode] {
			continue
		}

		occCode := getCell(row, 8)
		occTitle := getCell(row, 9)
		oGroup := getCell(row, 10)
		totEmp := parseNullableInt(getCell(row, 11))
		hMean := parseNullableFloat(getCell(row, 17))
		aMean := parseNullableFloat(getCell(row, 18))
		hMedian := parseNullableFloat(getCell(row, 22))
		aMedian := parseNullableFloat(getCell(row, 27))

		if _, err := stmt.Exec(
			stateCode, areaTitle, occCode, occTitle, oGroup,
			totEmp, hMean, aMean, hMedian, aMedian,
		); err != nil {
			log.Fatalf("Error inserting salary row %d: %v", i, err)
		}
		inserted++
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	return inserted
}

func loadRentData(db *sql.DB, filename string, validStates map[string]bool) int {
	f, err := excelize.OpenFile(filename)
	if err != nil {
		log.Fatalf("Failed to open %s: %v", filename, err)
	}
	defer f.Close()

	rows, err := f.GetRows("RentPerSquareFootAug2026")
	if err != nil {
		log.Fatalf("Failed to read sheet RentPerSquareFootAug2026: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO rent_prices (
			state_code, metro_name, period, rent_value, preceding_period, year_ago
		) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	inserted := 0
	for i, row := range rows {
		if i < 2 || len(row) < 5 {
			continue
		}
		metroName := strings.TrimSpace(row[0])
		period := strings.TrimSpace(row[1])
		val := parseNullableFloat(getCell(row, 2))
		prev := parseNullableFloat(getCell(row, 3))
		yearAgo := parseNullableFloat(getCell(row, 4))

		states := extractStateCodes(metroName, validStates)
		for _, st := range states {
			if _, err := stmt.Exec(st, metroName, period, val, prev, yearAgo); err != nil {
				log.Fatalf("Error inserting rent row %d (%s): %v", i, metroName, err)
			}
			inserted++
		}
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	return inserted
}

func loadHomePriceData(db *sql.DB, filename string, validStates map[string]bool) int {
	f, err := excelize.OpenFile(filename)
	if err != nil {
		log.Fatalf("Failed to open %s: %v", filename, err)
	}
	defer f.Close()

	rows, err := f.GetRows("hpi_master")
	if err != nil {
		log.Fatalf("Failed to read sheet hpi_master: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO home_prices (
			state_code, level, place_name, place_id, hpi_type, hpi_flavor,
			frequency, yr, period, index_nsa, index_sa
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	inserted := 0
	for i, row := range rows {
		// Ensure row has at least 10 columns (indices 0 through 9)
		if i == 0 || len(row) < 10 {
			continue
		}

		// Clean numeric strings before parsing (handles commas like "1,821")
		yrStr := strings.ReplaceAll(strings.TrimSpace(row[6]), ",", "")
		yr, err := strconv.Atoi(yrStr)
		if err != nil || yr != 2026 {
			continue
		}

		hpiType := getCell(row, 0)
		hpiFlavor := getCell(row, 1)
		frequency := getCell(row, 2)
		level := getCell(row, 3)
		placeName := getCell(row, 4)
		placeID := getCell(row, 5)

		periodStr := strings.ReplaceAll(getCell(row, 7), ",", "")
		period, err := strconv.Atoi(periodStr)
		if err != nil {
			continue // Skip row if period is non-numeric/invalid
		}

		indexNSA := parseNullableFloat(getCell(row, 8))
		indexSA := parseNullableFloat(getCell(row, 9))

		var targetStates []string
		switch level {
		case "State", "Puerto Rico":
			code := strings.ToUpper(placeID)
			if validStates[code] {
				targetStates = []string{code}
			}
		case "USA or Census Division":
			if strings.ToUpper(placeID) == "USA" {
				targetStates = []string{"US"}
			}
		case "MSA":
			targetStates = extractStateCodes(placeName, validStates)
		}

		for _, st := range targetStates {
			if _, err := stmt.Exec(
				st, level, placeName, placeID, hpiType, hpiFlavor,
				frequency, yr, period, indexNSA, indexSA,
			); err != nil {
				log.Fatalf("Error inserting HPI row %d (%s): %v", i, placeName, err)
			}
			inserted++
		}
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	return inserted
}

func extractStateCodes(location string, validStates map[string]bool) []string {
	idx := strings.LastIndex(location, ",")
	if idx == -1 {
		return nil
	}
	statePart := strings.TrimSpace(location[idx+1:])
	if parenIdx := strings.Index(statePart, "("); parenIdx != -1 {
		statePart = strings.TrimSpace(statePart[:parenIdx])
	}

	parts := strings.Split(statePart, "-")
	var result []string
	seen := make(map[string]bool)
	for _, p := range parts {
		code := strings.ToUpper(strings.TrimSpace(p))
		if validStates[code] && !seen[code] {
			seen[code] = true
			result = append(result, code)
		}
	}
	return result
}

func getCell(row []string, idx int) string {
	if idx < len(row) {
		return strings.TrimSpace(row[idx])
	}
	return ""
}

func parseNullableFloat(raw string) any {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, ",", "") // Remove thousands separators like "125,000.75"
	if raw == "" || raw == "*" || raw == "**" || raw == "#" || raw == "." {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return v
}

func parseNullableInt(raw string) any {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, ",", "") // Remove thousands separators like "1,234"
	if raw == "" || raw == "*" || raw == "**" || raw == "#" || raw == "." {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return v
}
