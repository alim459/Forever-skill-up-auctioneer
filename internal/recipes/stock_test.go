package recipes

import (
	"strings"
	"testing"
)

func TestParseVendorSQL(t *testing.T) {
	sql := "LOCK TABLES `npc_vendor` WRITE;\n" +
		"INSERT INTO `npc_vendor` VALUES (66,6270,1,9000,0,0,'Pattern: Blue Linen Vest'),(66,6260,0,0,15,0,'Blue Dye'),(4225,18949,1,3600,0,0,'Pattern: Barbaric Bracers'),(4589,18949,1,7200,0,0,'Pattern: Barbaric Bracers');\n" +
		"INSERT INTO `npc_vendor_template` VALUES (133,4357,4,10800,0,0,'Rough Blasting Powder');\n" +
		"INSERT INTO `creature` VALUES (17633,8385,0,1,490.9,1647.8,125.6,5.1,275,275,0,0);\n"
	l, err := ParseVendorSQL(strings.NewReader(sql))
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 3 || l[18949] != (Stock{1, 3600}) || l[6270] != (Stock{1, 9000}) || l[4357] != (Stock{4, 10800}) {
		t.Fatalf("%+v", l)
	}
	if _, ok := l[6260]; ok {
		t.Fatal("unlimited item included")
	}
	if _, ok := l[8385]; ok {
		t.Fatal("non-vendor table parsed")
	}
}
