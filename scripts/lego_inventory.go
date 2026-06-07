// Lego Inventory Builder
//
// Reads all order XML files from a data folder, cross-references parts
// and minifigures against their respective catalogs plus the colors catalog,
// aggregates quantities across all orders, and writes two output files:
//
//   - lego_parts_inventory.csv / .md      — parts (ITEMTYPE P)
//   - lego_minifigures_inventory.csv / .md — minifigures (ITEMTYPE M)
//
// Expected repository layout:
//
//	Lego/
//	├── scripts/
//	│   └── lego_inventory.go   ← this file
//	├── catalog/
//	│   ├── Parts.xml
//	│   ├── colors.xml
//	│   └── Minifigures.xml
//	├── data/                   ← drop order XML files here
//	└── output/                 ← generated inventory files land here
//
// Usage (run from any directory):
//
//	go run ./scripts/lego_inventory.go [flags]
//
// Flags (all optional — defaults match the layout above relative to the
// working directory):
//
//	-data     string   Folder containing order XML files  (default: ./data)
//	-catalog  string   Folder containing catalog XML files (default: ./catalog)
//	-out      string   Output folder                       (default: ./output)
package main

import (
	"encoding/csv"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// XML structs
// ---------------------------------------------------------------------------

// orderItem represents one <ITEM> element inside an order XML file.
type orderItem struct {
	ItemType string `xml:"ITEMTYPE"`
	ItemID   string `xml:"ITEMID"`
	ColorID  string `xml:"COLOR"`
	MinQty   int    `xml:"MINQTY"`
}

type orderInventory struct {
	Items []orderItem `xml:"ITEM"`
}

// ---------------------------------------------------------------------------
// Catalog loaders
// ---------------------------------------------------------------------------

// loadColors parses colors.xml and returns {colorID → colorName}.
func loadColors(path string) (map[string]string, error) {
	fmt.Printf("  Loading colors      : %s ... ", path)

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type colorItem struct {
		ColorID   string `xml:"COLOR"`
		ColorName string `xml:"COLORNAME"`
	}
	type catalog struct {
		Items []colorItem `xml:"ITEM"`
	}

	var c catalog
	if err := xml.NewDecoder(f).Decode(&c); err != nil {
		return nil, err
	}

	m := make(map[string]string, len(c.Items))
	for _, item := range c.Items {
		id := strings.TrimSpace(item.ColorID)
		if id != "" {
			m[id] = strings.TrimSpace(item.ColorName)
		}
	}
	fmt.Printf("%d entries\n", len(m))
	return m, nil
}

// catalogData holds the results of parsing a name catalog.
type catalogData struct {
	// names maps canonical ITEMID -> ITEMNAME.
	names map[string]string
	// aliases maps every archived/alternate ID -> canonical ITEMID.
	// e.g. "3068b" -> "3068", "1136" -> "3068", etc.
	aliases map[string]string
}

// loadNameCatalog streams an XML catalog file that has <ITEMID>, <ITEMNAME>,
// and <ALTITEMIDS> elements. It uses a token-based parser so it works
// efficiently on large files (e.g. Parts.xml ~26 MB).
//
// Returns a catalogData with:
//   - names:   {canonicalID -> itemName}
//   - aliases: {altID -> canonicalID}  (built from comma-separated ALTITEMIDS)
func loadNameCatalog(path, label string) (catalogData, error) {
	fmt.Printf("  Loading %-12s: %s ... ", label, path)

	f, err := os.Open(path)
	if err != nil {
		return catalogData{}, err
	}
	defer f.Close()

	names   := make(map[string]string, 100_000)
	aliases := make(map[string]string, 50_000)
	dec     := xml.NewDecoder(f)

	var inItem bool
	var itemID, itemName, altItemIDs, curField string

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return catalogData{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "ITEM":
				inItem = true
				itemID, itemName, altItemIDs, curField = "", "", "", ""
			case "ITEMID", "ITEMNAME", "ALTITEMIDS":
				if inItem {
					curField = t.Name.Local
				}
			}

		case xml.CharData:
			if inItem {
				val := strings.TrimSpace(string(t))
				switch curField {
				case "ITEMID":
					itemID = val
				case "ITEMNAME":
					itemName = val
				case "ALTITEMIDS":
					altItemIDs = val
				}
				curField = ""
			}

		case xml.EndElement:
			if t.Name.Local == "ITEM" && inItem {
				if itemID != "" {
					names[itemID] = itemName
					// Parse comma-separated alternate IDs, map each -> canonical
					for _, alt := range strings.Split(altItemIDs, ",") {
						alt = strings.TrimSpace(alt)
						if alt != "" && alt != itemID {
							aliases[alt] = itemID
						}
					}
				}
				inItem = false
			}
		}
	}

	fmt.Printf("%d entries, %d aliases\n", len(names), len(aliases))
	return catalogData{names: names, aliases: aliases}, nil
}

// resolveID returns the canonical item ID, following the alias map if the
// raw id is an archived alternate. Falls back to the raw id if not found.
func resolveID(id string, aliases map[string]string) string {
	if canonical, ok := aliases[id]; ok {
		return canonical
	}
	return id
}

// ---------------------------------------------------------------------------
// Order processing
// ---------------------------------------------------------------------------

type itemColorKey struct {
	ItemID  string
	ColorID string
}

type aggregated struct {
	parts  map[itemColorKey]int // ITEMTYPE P
	minifs map[itemColorKey]int // ITEMTYPE M
}

func processOrders(ordersFolder string, partAliases, minifAliases map[string]string) (aggregated, []string, error) {
	entries, err := os.ReadDir(ordersFolder)
	if err != nil {
		return aggregated{}, nil, fmt.Errorf("cannot read orders folder: %w", err)
	}

	agg := aggregated{
		parts:  make(map[itemColorKey]int),
		minifs: make(map[itemColorKey]int),
	}
	var filesRead []string
	totalResolved := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".xml") {
			continue
		}

		path := filepath.Join(ordersFolder, entry.Name())
		fmt.Printf("  Processing: %s\n", entry.Name())

		f, err := os.Open(path)
		if err != nil {
			fmt.Printf("    WARNING: cannot open %s: %v — skipping\n", entry.Name(), err)
			continue
		}

		var inv orderInventory
		if err := xml.NewDecoder(f).Decode(&inv); err != nil {
			fmt.Printf("    WARNING: cannot parse %s: %v — skipping\n", entry.Name(), err)
			f.Close()
			continue
		}
		f.Close()

		parts, minifs, resolved := 0, 0, 0
		for _, item := range inv.Items {
			id  := strings.TrimSpace(item.ItemID)
			cid := strings.TrimSpace(item.ColorID)
			qty := item.MinQty
			if qty == 0 {
				qty = 1
			}
			if id == "" {
				continue
			}

			itemType := strings.ToUpper(strings.TrimSpace(item.ItemType))
			switch itemType {
			case "M":
				canonical := resolveID(id, minifAliases)
				if canonical != id {
					fmt.Printf("    aliased minifig : %s -> %s\n", id, canonical)
					resolved++
				}
				agg.minifs[itemColorKey{canonical, cid}] += qty
				minifs++
			default: // "P" and anything else treated as a part
				canonical := resolveID(id, partAliases)
				if canonical != id {
					fmt.Printf("    aliased part    : %s -> %s\n", id, canonical)
					resolved++
				}
				agg.parts[itemColorKey{canonical, cid}] += qty
				parts++
			}
		}
		fmt.Printf("    %d part(s), %d minifigure(s), %d alias(es) resolved\n", parts, minifs, resolved)
		totalResolved += resolved
		filesRead = append(filesRead, entry.Name())
	}

	if len(filesRead) == 0 {
		return aggregated{}, nil, fmt.Errorf("no XML files found in '%s'", ordersFolder)
	}

	fmt.Printf("\n%d order file(s) processed — %d unique part combinations, %d unique minifigure combinations, %d total alias(es) resolved\n",
		len(filesRead), len(agg.parts), len(agg.minifs), totalResolved)
	return agg, filesRead, nil
}

// ---------------------------------------------------------------------------
// Row building & output
// ---------------------------------------------------------------------------

type row struct {
	ItemID    string
	ItemName  string
	ColorName string
	Qty       int
}

// buildRows resolves names from catalogs and sorts the result.
// For minifigures colorID is typically empty, so colorName will be "—".
func buildRows(src map[itemColorKey]int, nameCatalog, colorCatalog map[string]string) []row {
	rows := make([]row, 0, len(src))
	for key, qty := range src {
		name, ok := nameCatalog[key.ItemID]
		if !ok {
			name = fmt.Sprintf("UNKNOWN (%s)", key.ItemID)
		}

		var colorName string
		if key.ColorID == "" {
			colorName = "—"
		} else {
			colorName, ok = colorCatalog[key.ColorID]
			if !ok {
				colorName = fmt.Sprintf("UNKNOWN (%s)", key.ColorID)
			}
		}

		rows = append(rows, row{key.ItemID, name, colorName, qty})
	}

	sort.Slice(rows, func(i, j int) bool {
		ni := strings.ToLower(rows[i].ItemName)
		nj := strings.ToLower(rows[j].ItemName)
		if ni != nj {
			return ni < nj
		}
		return strings.ToLower(rows[i].ColorName) < strings.ToLower(rows[j].ColorName)
	})
	return rows
}

func writeCSV(rows []row, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	_ = w.Write([]string{"ITEM ID", "ITEM NAME", "ITEM COLOR", "QUANTITY"})
	for _, r := range rows {
		_ = w.Write([]string{r.ItemID, r.ItemName, r.ColorName, fmt.Sprintf("%d", r.Qty)})
	}
	w.Flush()
	fmt.Printf("  CSV      → %s\n", path)
	return w.Error()
}

func writeMarkdown(rows []row, path string, filesRead []string, title string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	total := 0
	for _, r := range rows {
		total += r.Qty
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", title))
	sb.WriteString(fmt.Sprintf("**Orders processed:** %d\n\n", len(filesRead)))
	for _, fn := range filesRead {
		sb.WriteString(fmt.Sprintf("- `%s`\n", fn))
	}
	sb.WriteString(fmt.Sprintf("\n**Unique (item × color) combinations:** %d  \n", len(rows)))
	sb.WriteString(fmt.Sprintf("**Total pieces:** %s\n\n", formatInt(total)))
	sb.WriteString("| ITEM ID | ITEM NAME | ITEM COLOR | QUANTITY |\n")
	sb.WriteString("|---------|-----------|------------|----------|\n")
	for _, r := range rows {
		name  := strings.ReplaceAll(r.ItemName,  "|", "\\|")
		color := strings.ReplaceAll(r.ColorName, "|", "\\|")
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %d |\n", r.ItemID, name, color, r.Qty))
	}

	_, err = f.WriteString(sb.String())
	if err == nil {
		fmt.Printf("  Markdown → %s\n", path)
	}
	return err
}

func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	ordersDir  := flag.String("data",    "./data",    "Folder containing order XML files")
	catalogDir := flag.String("catalog", "./catalog", "Folder containing catalog XML files")
	outDir     := flag.String("out",     "./output",  "Output folder")
	flag.Parse()

	// Derive catalog file paths
	partsFile  := filepath.Join(*catalogDir, "Parts.xml")
	colorsFile := filepath.Join(*catalogDir, "colors.xml")
	minifsFile := filepath.Join(*catalogDir, "Minifigures.xml")

	// Validate all required paths exist
	required := []struct{ path, label string }{
		{*ordersDir,  "data folder"},
		{*catalogDir, "catalog folder"},
		{partsFile,   "Parts.xml"},
		{colorsFile,  "colors.xml"},
		{minifsFile,  "Minifigures.xml"},
	}
	for _, r := range required {
		if _, err := os.Stat(r.path); os.IsNotExist(err) {
			log.Fatalf("ERROR: %s not found: '%s'", r.label, r.path)
		}
	}

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("ERROR: cannot create output folder: %v", err)
	}

	// Load catalogs
	fmt.Println("Loading catalogs ...")
	colorMap, err := loadColors(colorsFile)
	if err != nil {
		log.Fatalf("ERROR loading colors: %v", err)
	}
	partCatalog, err := loadNameCatalog(partsFile, "parts")
	if err != nil {
		log.Fatalf("ERROR loading parts: %v", err)
	}
	minifCatalog, err := loadNameCatalog(minifsFile, "minifigures")
	if err != nil {
		log.Fatalf("ERROR loading minifigures: %v", err)
	}

	// Process orders (archived IDs are resolved to canonical via alias maps)
	fmt.Printf("\nScanning data folder '%s' ...\n", *ordersDir)
	agg, filesRead, err := processOrders(*ordersDir, partCatalog.aliases, minifCatalog.aliases)
	if err != nil {
		log.Fatalf("ERROR processing orders: %v", err)
	}

	// Build rows (all IDs in agg are already canonical at this point)
	partRows  := buildRows(agg.parts,  partCatalog.names,  colorMap)
	minifRows := buildRows(agg.minifs, minifCatalog.names, colorMap)

	// Write parts output
	fmt.Println("\nWriting parts inventory ...")
	if err := writeCSV(partRows, filepath.Join(*outDir, "lego_parts_inventory.csv")); err != nil {
		log.Fatalf("ERROR writing parts CSV: %v", err)
	}
	if err := writeMarkdown(partRows, filepath.Join(*outDir, "lego_parts_inventory.md"),
		filesRead, "Lego Parts Inventory"); err != nil {
		log.Fatalf("ERROR writing parts Markdown: %v", err)
	}

	// Write minifigures output
	fmt.Println("\nWriting minifigures inventory ...")
	if err := writeCSV(minifRows, filepath.Join(*outDir, "lego_minifigures_inventory.csv")); err != nil {
		log.Fatalf("ERROR writing minifigures CSV: %v", err)
	}
	if err := writeMarkdown(minifRows, filepath.Join(*outDir, "lego_minifigures_inventory.md"),
		filesRead, "Lego Minifigures Inventory"); err != nil {
		log.Fatalf("ERROR writing minifigures Markdown: %v", err)
	}

	fmt.Println("\nDone.")
}
