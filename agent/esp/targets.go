package esp

import "strings"

type chipDef struct {
	Key                  string
	Name                 string
	ImageChipID          int
	UsesMagicValue       bool
	MagicValue           uint32
	MagicValueAlt        uint32
	BootloaderOffset     uint32
	SupportsFlashMD5     bool
	ExtendedFlashBegin   bool
	HasSecurityInfo      bool
	HasLegacySPIPads     bool
	IsESP8266            bool
	UARTDateReg          uint32
	UARTClkDivReg        uint32
	XTALClkDivider       uint32
	SPIRegBase           uint32
	SPIUsrOffs           uint32
	SPIUsr1Offs          uint32
	SPIUsr2Offs          uint32
	SPIMosiDlenOffs      uint32
	SPIMisoDlenOffs      uint32
	SPIW0Offs            uint32
	SPIAddrMSB           bool
	HasSPIDlenRegs       bool
	EFuseBase            uint32
	MACEFuseReg          uint32
	EFuseBlock0Base      uint32
	EFuseSecureBootReg   uint32
	EFuseSecureBootMask  uint32
	UARTDevBufNoReg      uint32
	UARTDevBufNoRegRev   uint32
	UARTDevBufNoUSBJTAG  uint32
	UARTDevBufNoUSBOTG   uint32
	WDTWProtectReg       uint32
	WDTConfig0Reg        uint32
	WDTConfig1Reg        uint32
	WDTWKey              uint32
	SWDWProtectReg       uint32
	SWDConfReg           uint32
	SWDAutoFeedEn        uint32
	SWDWKey              uint32
	GPIOStrapReg         uint32
	GPIOStrapSPIBootMask uint32
	RTCOption1Reg        uint32
	RTCForceDownloadMask uint32
	FlashSizes           map[string]byte
	FlashFreqs           map[string]byte
}

var stdFlashSizes = map[string]byte{
	"1MB": 0x00, "2MB": 0x10, "4MB": 0x20, "8MB": 0x30,
	"16MB": 0x40, "32MB": 0x50, "64MB": 0x60, "128MB": 0x70,
}

var stdFlashFreqs = map[string]byte{
	"80m": 0xF, "40m": 0x0, "26m": 0x1, "20m": 0x2,
}

var c6FlashFreqs = map[string]byte{
	"80m": 0x0, "40m": 0x0, "20m": 0x2,
}

var espChips = []chipDef{
	{
		Key: "esp8266", Name: "ESP8266", ImageChipID: -1, UsesMagicValue: true, MagicValue: 0xFFF0C101,
		BootloaderOffset: 0x0, IsESP8266: true,
		UARTClkDivReg: 0x60000014, XTALClkDivider: 2,
		SPIRegBase: 0x60000200, SPIUsrOffs: 0x1C, SPIUsr1Offs: 0x20, SPIUsr2Offs: 0x24, SPIW0Offs: 0x40, SPIAddrMSB: true,
		FlashSizes: map[string]byte{
			"512KB": 0x00, "256KB": 0x10, "1MB": 0x20, "2MB": 0x30, "4MB": 0x40,
			"2MB-c1": 0x50, "4MB-c1": 0x60, "8MB": 0x80, "16MB": 0x90,
		},
		FlashFreqs: stdFlashFreqs,
	},
	{
		Key: "esp32", Name: "ESP32", ImageChipID: 0, UsesMagicValue: true, MagicValue: 0x00F01D83,
		BootloaderOffset: 0x1000, HasLegacySPIPads: true,
		UARTDateReg: 0x60000078, UARTClkDivReg: 0x3FF40014, XTALClkDivider: 1,
		SPIRegBase: 0x3FF42000, SPIUsrOffs: 0x1C, SPIUsr1Offs: 0x20, SPIUsr2Offs: 0x24,
		SPIMosiDlenOffs: 0x28, SPIMisoDlenOffs: 0x2C, SPIW0Offs: 0x80, SPIAddrMSB: true, HasSPIDlenRegs: true,
		EFuseBase: 0x3FF5A000, EFuseBlock0Base: 0x3FF5A000,
		FlashSizes: stdFlashSizes, FlashFreqs: stdFlashFreqs,
	},
	{
		Key: "esp32s2", Name: "ESP32-S2", ImageChipID: 2, UsesMagicValue: true, MagicValue: 0x000007C6,
		BootloaderOffset: 0x1000, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x60000078, UARTClkDivReg: 0x3F400014, XTALClkDivider: 1,
		SPIRegBase: 0x3F402000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x3F41A000, MACEFuseReg: 0x3F41A044,
		UARTDevBufNoReg: 0x3FFFFD14, UARTDevBufNoUSBOTG: 2,
		WDTWProtectReg: 0x3F4080AC, WDTConfig0Reg: 0x3F408094, WDTConfig1Reg: 0x3F408098, WDTWKey: 0x50D83AA1,
		GPIOStrapReg: 0x3F404038, GPIOStrapSPIBootMask: 1 << 3, RTCOption1Reg: 0x3F408128, RTCForceDownloadMask: 0x1,
		FlashSizes: stdFlashSizes, FlashFreqs: stdFlashFreqs,
	},
	{
		Key: "esp32s3", Name: "ESP32-S3", ImageChipID: 9, MagicValue: 0x00000009,
		BootloaderOffset: 0x0, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x60000080, UARTClkDivReg: 0x60000014, XTALClkDivider: 1,
		SPIRegBase: 0x60002000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x60007000, MACEFuseReg: 0x60007044,
		UARTDevBufNoReg: 0x3FCEF14C, UARTDevBufNoUSBOTG: 3, UARTDevBufNoUSBJTAG: 4,
		WDTWProtectReg: 0x600080B0, WDTConfig0Reg: 0x60008098, WDTConfig1Reg: 0x6000809C, WDTWKey: 0x50D83AA1,
		SWDWProtectReg: 0x600080B8, SWDConfReg: 0x600080B4, SWDAutoFeedEn: 1 << 31, SWDWKey: 0x8F1D312A,
		GPIOStrapReg: 0x60004038, GPIOStrapSPIBootMask: 1 << 3, RTCOption1Reg: 0x6000812C, RTCForceDownloadMask: 0x1,
		FlashSizes: stdFlashSizes, FlashFreqs: stdFlashFreqs,
	},
	{
		Key: "esp32c3", Name: "ESP32-C3", ImageChipID: 5, MagicValue: 0x6921506F, MagicValueAlt: 0x1B31506F,
		BootloaderOffset: 0x0, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x6000007C, UARTClkDivReg: 0x60000014, XTALClkDivider: 1,
		SPIRegBase: 0x60002000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x60008800, MACEFuseReg: 0x60008844,
		UARTDevBufNoReg: 0x3FCDF064 + 24, UARTDevBufNoRegRev: 0x3FCDF060 + 24, UARTDevBufNoUSBJTAG: 3,
		WDTWProtectReg: 0x600080A8, WDTConfig0Reg: 0x60008090, WDTConfig1Reg: 0x60008094, WDTWKey: 0x50D83AA1,
		SWDWProtectReg: 0x600080B0, SWDConfReg: 0x600080AC, SWDAutoFeedEn: 1 << 31, SWDWKey: 0x8F1D312A,
		FlashSizes: stdFlashSizes, FlashFreqs: stdFlashFreqs,
	},
	{
		Key: "esp32c2", Name: "ESP32-C2", ImageChipID: 12,
		BootloaderOffset: 0x0, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x6000007C, UARTClkDivReg: 0x60000014, XTALClkDivider: 1,
		SPIRegBase: 0x60002000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x60008800, MACEFuseReg: 0x60008840,
		WDTWProtectReg: 0x6000809C, WDTConfig0Reg: 0x60008084, WDTConfig1Reg: 0x60008088, WDTWKey: 0x50D83AA1,
		FlashSizes: stdFlashSizes,
		FlashFreqs: map[string]byte{"60m": 0xF, "30m": 0x0, "20m": 0x1, "15m": 0x2},
	},
	{
		Key: "esp32c6", Name: "ESP32-C6", ImageChipID: 13,
		BootloaderOffset: 0x0, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x6000007C, UARTClkDivReg: 0x60000014, XTALClkDivider: 1,
		SPIRegBase: 0x60003000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x600B0800, MACEFuseReg: 0x600B0844,
		UARTDevBufNoReg: 0x4087F580, UARTDevBufNoUSBJTAG: 3,
		WDTWProtectReg: 0x600B1C18, WDTConfig0Reg: 0x600B1C00, WDTConfig1Reg: 0x600B1C04, WDTWKey: 0x50D83AA1,
		SWDWProtectReg: 0x600B1C20, SWDConfReg: 0x600B1C1C, SWDAutoFeedEn: 1 << 18, SWDWKey: 0x50D83AA1,
		FlashSizes: stdFlashSizes, FlashFreqs: c6FlashFreqs,
	},
	{
		Key: "esp32h2", Name: "ESP32-H2", ImageChipID: 16,
		BootloaderOffset: 0x0, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x6000007C, UARTClkDivReg: 0x60000014, XTALClkDivider: 1,
		SPIRegBase: 0x60003000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x600B0800, MACEFuseReg: 0x600B0844,
		UARTDevBufNoReg: 0x4084FEFC, UARTDevBufNoUSBJTAG: 3,
		WDTWProtectReg: 0x600B1C1C, WDTConfig0Reg: 0x600B1C00, WDTConfig1Reg: 0x600B1C04, WDTWKey: 0x50D83AA1,
		SWDWProtectReg: 0x600B1C24, SWDConfReg: 0x600B1C20, SWDAutoFeedEn: 1 << 18, SWDWKey: 0x50D83AA1,
		FlashSizes: stdFlashSizes,
		FlashFreqs: map[string]byte{"48m": 0xF, "24m": 0x0, "16m": 0x1, "12m": 0x2},
	},
	{
		Key: "esp32p4", Name: "ESP32-P4", ImageChipID: 18,
		BootloaderOffset: 0x2000, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x500CA08C, UARTClkDivReg: 0x500CA014, XTALClkDivider: 1,
		SPIRegBase: 0x5008D000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x5012D000, MACEFuseReg: 0x5012D044,
		UARTDevBufNoReg: 0x4FF3FEB0 + 24, UARTDevBufNoRegRev: 0x4FFBFEB0 + 24, UARTDevBufNoUSBOTG: 5, UARTDevBufNoUSBJTAG: 6,
		WDTWProtectReg: 0x50116018, WDTConfig0Reg: 0x50116000, WDTConfig1Reg: 0x50116004, WDTWKey: 0x50D83AA1,
		SWDWProtectReg: 0x50116020, SWDConfReg: 0x5011601C, SWDAutoFeedEn: 1 << 18, SWDWKey: 0x50D83AA1,
		GPIOStrapReg: 0x500E0038, GPIOStrapSPIBootMask: 0x8, RTCOption1Reg: 0x50110008, RTCForceDownloadMask: 0x4,
		FlashSizes: stdFlashSizes, FlashFreqs: c6FlashFreqs,
	},
	{
		Key: "esp32c61", Name: "ESP32-C61", ImageChipID: 20,
		BootloaderOffset: 0x0, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x6000007C, UARTClkDivReg: 0x60000014, XTALClkDivider: 1,
		SPIRegBase: 0x60003000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x600B4800, MACEFuseReg: 0x600B4844,
		UARTDevBufNoReg: 0x4084F5EC, UARTDevBufNoRegRev: 0x4084F5E4, UARTDevBufNoUSBJTAG: 3,
		WDTWProtectReg: 0x600B1C18, WDTConfig0Reg: 0x600B1C00, WDTConfig1Reg: 0x600B1C04, WDTWKey: 0x50D83AA1,
		SWDWProtectReg: 0x600B1C20, SWDConfReg: 0x600B1C1C, SWDAutoFeedEn: 1 << 18, SWDWKey: 0x50D83AA1,
		FlashSizes: stdFlashSizes, FlashFreqs: c6FlashFreqs,
	},
	{
		Key: "esp32c5", Name: "ESP32-C5", ImageChipID: 23,
		BootloaderOffset: 0x2000, SupportsFlashMD5: true, ExtendedFlashBegin: true, HasSecurityInfo: true,
		UARTDateReg: 0x6000007C, UARTClkDivReg: 0x60000014, XTALClkDivider: 1,
		SPIRegBase: 0x60003000, SPIUsrOffs: 0x18, SPIUsr1Offs: 0x1C, SPIUsr2Offs: 0x20,
		SPIMosiDlenOffs: 0x24, SPIMisoDlenOffs: 0x28, SPIW0Offs: 0x58, HasSPIDlenRegs: true,
		EFuseBase: 0x600B4800, MACEFuseReg: 0x600B4844,
		UARTDevBufNoReg: 0x4085F514, UARTDevBufNoUSBJTAG: 3,
		WDTWProtectReg: 0x600B1C18, WDTConfig0Reg: 0x600B1C00, WDTConfig1Reg: 0x600B1C04, WDTWKey: 0x50D83AA1,
		SWDWProtectReg: 0x600B1C20, SWDConfReg: 0x600B1C1C, SWDAutoFeedEn: 1 << 18, SWDWKey: 0x50D83AA1,
		FlashSizes: stdFlashSizes,
		FlashFreqs: map[string]byte{"80m": 0xF, "40m": 0x0, "20m": 0x2},
	},
}

func chipByKey(key string) (chipDef, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, chip := range espChips {
		if chip.Key == key {
			return chip, true
		}
	}
	return chipDef{}, false
}

func chipByID(id int) (chipDef, bool) {
	for _, chip := range espChips {
		if !chip.UsesMagicValue && chip.ImageChipID == id {
			return chip, true
		}
	}
	return chipDef{}, false
}

func chipByMagic(magic uint32) (chipDef, bool) {
	for _, chip := range espChips {
		if chip.MagicValue == magic || (chip.MagicValueAlt != 0 && chip.MagicValueAlt == magic) {
			return chip, true
		}
	}
	return chipDef{}, false
}
