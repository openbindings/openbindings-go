// Code generated from the Unicode 13.0.0 character database
// (PropertyAliases.txt, PropertyValueAliases.txt); DO NOT EDIT.

package schemacompiler

// The property names and values a Unicode property escape may use in a
// pattern read under ECMA-262 11th edition with the u flag (§21.2.2.8.2,
// Tables 53 to 56), for Unicode 13.0. They decide only whether a pattern is
// well formed; which characters a property matches is the evaluator's.

// binaryProperties are the binary properties and their aliases (Table 54),
// with ASCII, Any, and Assigned.
var binaryProperties = setOf(
	"AHex", "ASCII", "ASCII_Hex_Digit", "Alpha", "Alphabetic", "Any",
	"Assigned", "Bidi_C", "Bidi_Control", "Bidi_M", "Bidi_Mirrored", "CI",
	"CWCF", "CWCM", "CWKCF", "CWL", "CWT", "CWU", "Case_Ignorable", "Cased",
	"Changes_When_Casefolded", "Changes_When_Casemapped",
	"Changes_When_Lowercased", "Changes_When_NFKC_Casefolded",
	"Changes_When_Titlecased", "Changes_When_Uppercased", "DI", "Dash",
	"Default_Ignorable_Code_Point", "Dep", "Deprecated", "Dia", "Diacritic",
	"EBase", "EComp", "EMod", "EPres", "Emoji", "Emoji_Component",
	"Emoji_Modifier", "Emoji_Modifier_Base", "Emoji_Presentation", "Ext",
	"ExtPict", "Extended_Pictographic", "Extender", "Gr_Base", "Gr_Ext",
	"Grapheme_Base", "Grapheme_Extend", "Hex", "Hex_Digit", "IDC", "IDS",
	"IDSB", "IDST", "IDS_Binary_Operator", "IDS_Trinary_Operator",
	"ID_Continue", "ID_Start", "Ideo", "Ideographic", "Join_C", "Join_Control",
	"LOE", "Logical_Order_Exception", "Lower", "Lowercase", "Math", "NChar",
	"Noncharacter_Code_Point", "Pat_Syn", "Pat_WS", "Pattern_Syntax",
	"Pattern_White_Space", "QMark", "Quotation_Mark", "RI", "Radical",
	"Regional_Indicator", "SD", "STerm", "Sentence_Terminal", "Soft_Dotted",
	"Term", "Terminal_Punctuation", "UIdeo", "Unified_Ideograph", "Upper",
	"Uppercase", "VS", "Variation_Selector", "WSpace", "White_Space", "XIDC",
	"XIDS", "XID_Continue", "XID_Start", "space",
)

// generalCategoryValues are the General_Category values and their aliases
// (Table 55).
var generalCategoryValues = setOf(
	"C", "Cased_Letter", "Cc", "Cf", "Close_Punctuation", "Cn", "Co",
	"Combining_Mark", "Connector_Punctuation", "Control", "Cs",
	"Currency_Symbol", "Dash_Punctuation", "Decimal_Number", "Enclosing_Mark",
	"Final_Punctuation", "Format", "Initial_Punctuation", "L", "LC", "Letter",
	"Letter_Number", "Line_Separator", "Ll", "Lm", "Lo", "Lowercase_Letter",
	"Lt", "Lu", "M", "Mark", "Math_Symbol", "Mc", "Me", "Mn", "Modifier_Letter",
	"Modifier_Symbol", "N", "Nd", "Nl", "No", "Nonspacing_Mark", "Number",
	"Open_Punctuation", "Other", "Other_Letter", "Other_Number",
	"Other_Punctuation", "Other_Symbol", "P", "Paragraph_Separator", "Pc", "Pd",
	"Pe", "Pf", "Pi", "Po", "Private_Use", "Ps", "Punctuation", "S", "Sc",
	"Separator", "Sk", "Sm", "So", "Space_Separator", "Spacing_Mark",
	"Surrogate", "Symbol", "Titlecase_Letter", "Unassigned", "Uppercase_Letter",
	"Z", "Zl", "Zp", "Zs", "cntrl", "digit", "punct",
)

// scriptValues are the Script and Script_Extensions values and their
// aliases (Table 56).
var scriptValues = setOf(
	"Adlam", "Adlm", "Aghb", "Ahom", "Anatolian_Hieroglyphs", "Arab", "Arabic",
	"Armenian", "Armi", "Armn", "Avestan", "Avst", "Bali", "Balinese", "Bamu",
	"Bamum", "Bass", "Bassa_Vah", "Batak", "Batk", "Beng", "Bengali",
	"Bhaiksuki", "Bhks", "Bopo", "Bopomofo", "Brah", "Brahmi", "Brai",
	"Braille", "Bugi", "Buginese", "Buhd", "Buhid", "Cakm",
	"Canadian_Aboriginal", "Cans", "Cari", "Carian", "Caucasian_Albanian",
	"Chakma", "Cham", "Cher", "Cherokee", "Chorasmian", "Chrs", "Common",
	"Copt", "Coptic", "Cprt", "Cuneiform", "Cypriot", "Cyrillic", "Cyrl",
	"Deseret", "Deva", "Devanagari", "Diak", "Dives_Akuru", "Dogr", "Dogra",
	"Dsrt", "Dupl", "Duployan", "Egyp", "Egyptian_Hieroglyphs", "Elba",
	"Elbasan", "Elym", "Elymaic", "Ethi", "Ethiopic", "Geor", "Georgian",
	"Glag", "Glagolitic", "Gong", "Gonm", "Goth", "Gothic", "Gran", "Grantha",
	"Greek", "Grek", "Gujarati", "Gujr", "Gunjala_Gondi", "Gurmukhi", "Guru",
	"Han", "Hang", "Hangul", "Hani", "Hanifi_Rohingya", "Hano", "Hanunoo",
	"Hatr", "Hatran", "Hebr", "Hebrew", "Hira", "Hiragana", "Hluw", "Hmng",
	"Hmnp", "Hrkt", "Hung", "Imperial_Aramaic", "Inherited",
	"Inscriptional_Pahlavi", "Inscriptional_Parthian", "Ital", "Java",
	"Javanese", "Kaithi", "Kali", "Kana", "Kannada", "Katakana",
	"Katakana_Or_Hiragana", "Kayah_Li", "Khar", "Kharoshthi",
	"Khitan_Small_Script", "Khmer", "Khmr", "Khoj", "Khojki", "Khudawadi",
	"Kits", "Knda", "Kthi", "Lana", "Lao", "Laoo", "Latin", "Latn", "Lepc",
	"Lepcha", "Limb", "Limbu", "Lina", "Linb", "Linear_A", "Linear_B", "Lisu",
	"Lyci", "Lycian", "Lydi", "Lydian", "Mahajani", "Mahj", "Maka", "Makasar",
	"Malayalam", "Mand", "Mandaic", "Mani", "Manichaean", "Marc", "Marchen",
	"Masaram_Gondi", "Medefaidrin", "Medf", "Meetei_Mayek", "Mend",
	"Mende_Kikakui", "Merc", "Mero", "Meroitic_Cursive", "Meroitic_Hieroglyphs",
	"Miao", "Mlym", "Modi", "Mong", "Mongolian", "Mro", "Mroo", "Mtei", "Mult",
	"Multani", "Myanmar", "Mymr", "Nabataean", "Nand", "Nandinagari", "Narb",
	"Nbat", "New_Tai_Lue", "Newa", "Nko", "Nkoo", "Nshu", "Nushu",
	"Nyiakeng_Puachue_Hmong", "Ogam", "Ogham", "Ol_Chiki", "Olck",
	"Old_Hungarian", "Old_Italic", "Old_North_Arabian", "Old_Permic",
	"Old_Persian", "Old_Sogdian", "Old_South_Arabian", "Old_Turkic", "Oriya",
	"Orkh", "Orya", "Osage", "Osge", "Osma", "Osmanya", "Pahawh_Hmong", "Palm",
	"Palmyrene", "Pau_Cin_Hau", "Pauc", "Perm", "Phag", "Phags_Pa", "Phli",
	"Phlp", "Phnx", "Phoenician", "Plrd", "Prti", "Psalter_Pahlavi", "Qaac",
	"Qaai", "Rejang", "Rjng", "Rohg", "Runic", "Runr", "Samaritan", "Samr",
	"Sarb", "Saur", "Saurashtra", "Sgnw", "Sharada", "Shavian", "Shaw", "Shrd",
	"Sidd", "Siddham", "SignWriting", "Sind", "Sinh", "Sinhala", "Sogd",
	"Sogdian", "Sogo", "Sora", "Sora_Sompeng", "Soyo", "Soyombo", "Sund",
	"Sundanese", "Sylo", "Syloti_Nagri", "Syrc", "Syriac", "Tagalog", "Tagb",
	"Tagbanwa", "Tai_Le", "Tai_Tham", "Tai_Viet", "Takr", "Takri", "Tale",
	"Talu", "Tamil", "Taml", "Tang", "Tangut", "Tavt", "Telu", "Telugu", "Tfng",
	"Tglg", "Thaa", "Thaana", "Thai", "Tibetan", "Tibt", "Tifinagh", "Tirh",
	"Tirhuta", "Ugar", "Ugaritic", "Unknown", "Vai", "Vaii", "Wancho", "Wara",
	"Warang_Citi", "Wcho", "Xpeo", "Xsux", "Yezi", "Yezidi", "Yi", "Yiii",
	"Zanabazar_Square", "Zanb", "Zinh", "Zyyy", "Zzzz",
)

// nonBinaryProperties maps each non-binary property name and alias (Table 53)
// to its values.
var nonBinaryProperties = map[string]map[string]bool{
	"General_Category": generalCategoryValues, "gc": generalCategoryValues,
	"Script": scriptValues, "sc": scriptValues,
	"Script_Extensions": scriptValues, "scx": scriptValues,
}

func setOf(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}
