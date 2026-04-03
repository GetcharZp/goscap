//go:build windows

package goscap

import (
	"errors"
	"github.com/up-zero/gotool/convertutil"
	"image"
	"math"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// dxgiCapturer provides DXGI desktop duplication for Windows.
// The mutex keeps the D3D11 immediate context single-threaded.
type dxgiCapturer struct {
	mu      sync.Mutex
	width   int
	height  int
	timeout time.Duration

	factory     *IDXGIFactory1
	adapter     *IDXGIAdapter1
	output      *IDXGIOutput
	output1     *IDXGIOutput1
	duplication *IDXGIOutputDuplication

	device  *ID3D11Device
	context *ID3D11DeviceContext

	staging       *ID3D11Texture2D
	stagingFormat uint32
	stagingWidth  int
	stagingHeight int

	lastImage *image.RGBA
}

type HRESULT uint32

func failed(hr HRESULT) bool { return int32(hr) < 0 }

const (
	coinitMultithreaded = 0x0

	d3d11CreateDeviceBgraSupport = 0x20
	d3d11SdkVersion              = 7

	d3dDriverTypeUnknown = 0

	d3d11UsageStaging  = 3
	d3d11CpuAccessRead = 0x20000
	d3d11BindFlagNone  = 0
	d3d11MiscFlagNone  = 0

	dxgiFormatR16G16B16A16Float = 10
	dxgiFormatR10G10B10A2Unorm  = 24
	dxgiFormatR8G8B8A8Unorm     = 28
	dxgiFormatR8G8B8A8UnormSRGB = 29
	dxgiFormatB8G8R8A8Unorm     = 87
	dxgiFormatB8G8R8A8UnormSRGB = 91

	dxgiErrorWaitTimeout = 0x887A0027
	dxgiErrorAccessLost  = 0x887A0026
)

var (
	errAccessLost = errors.New("dxgi access lost")
)

var (
	modDXGI  = syscall.NewLazyDLL("dxgi.dll")
	modD3D11 = syscall.NewLazyDLL("d3d11.dll")
	modOle32 = syscall.NewLazyDLL("ole32.dll")

	procCreateDXGIFactory1 = modDXGI.NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice  = modD3D11.NewProc("D3D11CreateDevice")
	procCoInitializeEx     = modOle32.NewProc("CoInitializeEx")
)

type windowsGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	iidIDXGIFactory1   = windowsGUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidIDXGIOutput1    = windowsGUID{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = windowsGUID{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

type IUnknown struct {
	lpVtbl *IUnknownVtbl
}

type IUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

func (u *IUnknown) QueryInterface(riid *windowsGUID, ppv unsafe.Pointer) HRESULT {
	ret, _, _ := syscall.SyscallN(u.lpVtbl.QueryInterface, uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(riid)), uintptr(ppv))
	return HRESULT(ret)
}

func (u *IUnknown) Release() uint32 {
	ret, _, _ := syscall.SyscallN(u.lpVtbl.Release, uintptr(unsafe.Pointer(u)))
	return uint32(ret)
}

type IDXGIFactory1 struct{ lpVtbl *IDXGIFactory1Vtbl }

type IDXGIFactory1Vtbl struct {
	QueryInterface          uintptr
	AddRef                  uintptr
	Release                 uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	GetPrivateData          uintptr
	GetParent               uintptr
	EnumAdapters            uintptr
	MakeWindowAssociation   uintptr
	GetWindowAssociation    uintptr
	CreateSwapChain         uintptr
	CreateSoftwareAdapter   uintptr
	EnumAdapters1           uintptr
	IsCurrent               uintptr
}

func (f *IDXGIFactory1) Release() uint32 {
	ret, _, _ := syscall.SyscallN(f.lpVtbl.Release, uintptr(unsafe.Pointer(f)))
	return uint32(ret)
}

func (f *IDXGIFactory1) EnumAdapters1(index uint32, adapter **IDXGIAdapter1) HRESULT {
	ret, _, _ := syscall.SyscallN(f.lpVtbl.EnumAdapters1, uintptr(unsafe.Pointer(f)), uintptr(index), uintptr(unsafe.Pointer(adapter)))
	return HRESULT(ret)
}

type IDXGIAdapter1 struct{ lpVtbl *IDXGIAdapter1Vtbl }

type IDXGIAdapter1Vtbl struct {
	QueryInterface          uintptr
	AddRef                  uintptr
	Release                 uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	GetPrivateData          uintptr
	GetParent               uintptr
	EnumOutputs             uintptr
	GetDesc                 uintptr
	CheckInterfaceSupport   uintptr
	GetDesc1                uintptr
}

func (a *IDXGIAdapter1) Release() uint32 {
	ret, _, _ := syscall.SyscallN(a.lpVtbl.Release, uintptr(unsafe.Pointer(a)))
	return uint32(ret)
}

func (a *IDXGIAdapter1) EnumOutputs(index uint32, output **IDXGIOutput) HRESULT {
	ret, _, _ := syscall.SyscallN(a.lpVtbl.EnumOutputs, uintptr(unsafe.Pointer(a)), uintptr(index), uintptr(unsafe.Pointer(output)))
	return HRESULT(ret)
}

type IDXGIOutput struct{ lpVtbl *IDXGIOutputVtbl }

type IDXGIOutputVtbl struct {
	QueryInterface              uintptr
	AddRef                      uintptr
	Release                     uintptr
	SetPrivateData              uintptr
	SetPrivateDataInterface     uintptr
	GetPrivateData              uintptr
	GetParent                   uintptr
	GetDesc                     uintptr
	GetDisplayModeList          uintptr
	FindClosestMatchingMode     uintptr
	WaitForVBlank               uintptr
	TakeOwnership               uintptr
	ReleaseOwnership            uintptr
	GetGammaControlCapabilities uintptr
	SetGammaControl             uintptr
	GetGammaControl             uintptr
	SetDisplaySurface           uintptr
	GetDisplaySurfaceData       uintptr
	GetFrameStatistics          uintptr
}

func (o *IDXGIOutput) Release() uint32 {
	ret, _, _ := syscall.SyscallN(o.lpVtbl.Release, uintptr(unsafe.Pointer(o)))
	return uint32(ret)
}

func (o *IDXGIOutput) GetDesc(desc *DxgiOutputDesc) HRESULT {
	ret, _, _ := syscall.SyscallN(o.lpVtbl.GetDesc, uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(desc)))
	return HRESULT(ret)
}

func (o *IDXGIOutput) QueryInterface(riid *windowsGUID, out unsafe.Pointer) HRESULT {
	ret, _, _ := syscall.SyscallN(o.lpVtbl.QueryInterface, uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(riid)), uintptr(out))
	return HRESULT(ret)
}

type IDXGIOutput1 struct{ lpVtbl *IDXGIOutput1Vtbl }

type IDXGIOutput1Vtbl struct {
	QueryInterface              uintptr
	AddRef                      uintptr
	Release                     uintptr
	SetPrivateData              uintptr
	SetPrivateDataInterface     uintptr
	GetPrivateData              uintptr
	GetParent                   uintptr
	GetDesc                     uintptr
	GetDisplayModeList          uintptr
	FindClosestMatchingMode     uintptr
	WaitForVBlank               uintptr
	TakeOwnership               uintptr
	ReleaseOwnership            uintptr
	GetGammaControlCapabilities uintptr
	SetGammaControl             uintptr
	GetGammaControl             uintptr
	SetDisplaySurface           uintptr
	GetDisplaySurfaceData       uintptr
	GetFrameStatistics          uintptr
	GetDisplayModeList1         uintptr
	FindClosestMatchingMode1    uintptr
	GetDisplaySurfaceData1      uintptr
	DuplicateOutput             uintptr
}

func (o *IDXGIOutput1) Release() uint32 {
	ret, _, _ := syscall.SyscallN(o.lpVtbl.Release, uintptr(unsafe.Pointer(o)))
	return uint32(ret)
}

func (o *IDXGIOutput1) DuplicateOutput(device *ID3D11Device, dup **IDXGIOutputDuplication) HRESULT {
	ret, _, _ := syscall.SyscallN(o.lpVtbl.DuplicateOutput, uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(dup)))
	return HRESULT(ret)
}

type IDXGIOutputDuplication struct{ lpVtbl *IDXGIOutputDuplicationVtbl }

type IDXGIOutputDuplicationVtbl struct {
	QueryInterface          uintptr
	AddRef                  uintptr
	Release                 uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	GetPrivateData          uintptr
	GetParent               uintptr
	GetDesc                 uintptr
	AcquireNextFrame        uintptr
	GetFrameDirtyRects      uintptr
	GetFrameMoveRects       uintptr
	GetFramePointerShape    uintptr
	MapDesktopSurface       uintptr
	UnMapDesktopSurface     uintptr
	ReleaseFrame            uintptr
}

func (d *IDXGIOutputDuplication) Release() uint32 {
	ret, _, _ := syscall.SyscallN(d.lpVtbl.Release, uintptr(unsafe.Pointer(d)))
	return uint32(ret)
}

func (d *IDXGIOutputDuplication) AcquireNextFrame(timeoutMS uint32, info *DxgiOutduplFrameInfo, resource **IDXGIResource) HRESULT {
	ret, _, _ := syscall.SyscallN(d.lpVtbl.AcquireNextFrame, uintptr(unsafe.Pointer(d)), uintptr(timeoutMS), uintptr(unsafe.Pointer(info)), uintptr(unsafe.Pointer(resource)))
	return HRESULT(ret)
}

func (d *IDXGIOutputDuplication) ReleaseFrame() HRESULT {
	ret, _, _ := syscall.SyscallN(d.lpVtbl.ReleaseFrame, uintptr(unsafe.Pointer(d)))
	return HRESULT(ret)
}

func (d *IDXGIOutputDuplication) GetDesc(desc *DxgiOutduplDesc) HRESULT {
	ret, _, _ := syscall.SyscallN(d.lpVtbl.GetDesc, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(desc)))
	return HRESULT(ret)
}

type IDXGIResource struct{ lpVtbl *IDXGIResourceVtbl }

type IDXGIResourceVtbl struct {
	QueryInterface          uintptr
	AddRef                  uintptr
	Release                 uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	GetPrivateData          uintptr
	GetParent               uintptr
	GetDevice               uintptr
	GetSharedHandle         uintptr
	GetUsage                uintptr
	SetEvictionPriority     uintptr
	GetEvictionPriority     uintptr
}

func (r *IDXGIResource) Release() uint32 {
	ret, _, _ := syscall.SyscallN(r.lpVtbl.Release, uintptr(unsafe.Pointer(r)))
	return uint32(ret)
}

func (r *IDXGIResource) QueryInterface(riid *windowsGUID, out unsafe.Pointer) HRESULT {
	ret, _, _ := syscall.SyscallN(r.lpVtbl.QueryInterface, uintptr(unsafe.Pointer(r)), uintptr(unsafe.Pointer(riid)), uintptr(out))
	return HRESULT(ret)
}

type ID3D11Device struct{ lpVtbl *ID3D11DeviceVtbl }

type ID3D11DeviceVtbl struct {
	QueryInterface                       uintptr
	AddRef                               uintptr
	Release                              uintptr
	CreateBuffer                         uintptr
	CreateTexture1D                      uintptr
	CreateTexture2D                      uintptr
	CreateTexture3D                      uintptr
	CreateShaderResourceView             uintptr
	CreateUnorderedAccessView            uintptr
	CreateRenderTargetView               uintptr
	CreateDepthStencilView               uintptr
	CreateInputLayout                    uintptr
	CreateVertexShader                   uintptr
	CreateGeometryShader                 uintptr
	CreateGeometryShaderWithStreamOutput uintptr
	CreatePixelShader                    uintptr
	CreateHullShader                     uintptr
	CreateDomainShader                   uintptr
	CreateComputeShader                  uintptr
	CreateClassLinkage                   uintptr
	CreateBlendState                     uintptr
	CreateDepthStencilState              uintptr
	CreateRasterizerState                uintptr
	CreateSamplerState                   uintptr
	CreateQuery                          uintptr
	CreatePredicate                      uintptr
	CreateCounter                        uintptr
	CreateDeferredContext                uintptr
	OpenSharedResource                   uintptr
	CheckFormatSupport                   uintptr
	CheckMultisampleQualityLevels        uintptr
	CheckCounterInfo                     uintptr
	CheckCounter                         uintptr
	CheckFeatureSupport                  uintptr
	GetPrivateData                       uintptr
	SetPrivateData                       uintptr
	SetPrivateDataInterface              uintptr
	GetFeatureLevel                      uintptr
	GetCreationFlags                     uintptr
	GetDeviceRemovedReason               uintptr
	GetImmediateContext                  uintptr
	SetExceptionMode                     uintptr
	GetExceptionMode                     uintptr
}

func (d *ID3D11Device) Release() uint32 {
	ret, _, _ := syscall.SyscallN(d.lpVtbl.Release, uintptr(unsafe.Pointer(d)))
	return uint32(ret)
}

func (d *ID3D11Device) CreateTexture2D(desc *D3d11Texture2dDesc, data *D3d11SubresourceData, tex **ID3D11Texture2D) HRESULT {
	ret, _, _ := syscall.SyscallN(d.lpVtbl.CreateTexture2D, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(desc)), uintptr(unsafe.Pointer(data)), uintptr(unsafe.Pointer(tex)))
	return HRESULT(ret)
}

func (d *ID3D11Device) GetImmediateContext(ctx **ID3D11DeviceContext) {
	syscall.SyscallN(d.lpVtbl.GetImmediateContext, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(ctx)))
}

type ID3D11DeviceContext struct{ lpVtbl *ID3D11DeviceContextVtbl }

type ID3D11DeviceContextVtbl struct {
	QueryInterface                            uintptr
	AddRef                                    uintptr
	Release                                   uintptr
	GetDevice                                 uintptr
	GetPrivateData                            uintptr
	SetPrivateData                            uintptr
	SetPrivateDataInterface                   uintptr
	VSSetConstantBuffers                      uintptr
	PSSetShaderResources                      uintptr
	PSSetShader                               uintptr
	PSSetSamplers                             uintptr
	VSSetShader                               uintptr
	DrawIndexed                               uintptr
	Draw                                      uintptr
	Map                                       uintptr
	Unmap                                     uintptr
	PSSetConstantBuffers                      uintptr
	IASetInputLayout                          uintptr
	IASetVertexBuffers                        uintptr
	IASetIndexBuffer                          uintptr
	DrawIndexedInstanced                      uintptr
	DrawInstanced                             uintptr
	GSSetConstantBuffers                      uintptr
	GSSetShader                               uintptr
	IASetPrimitiveTopology                    uintptr
	VSSetShaderResources                      uintptr
	VSSetSamplers                             uintptr
	Begin                                     uintptr
	End                                       uintptr
	GetData                                   uintptr
	SetPredication                            uintptr
	GSSetShaderResources                      uintptr
	GSSetSamplers                             uintptr
	OMSetRenderTargets                        uintptr
	OMSetRenderTargetsAndUnorderedAccessViews uintptr
	OMSetBlendState                           uintptr
	OMSetDepthStencilState                    uintptr
	SOSetTargets                              uintptr
	DrawAuto                                  uintptr
	DrawIndexedInstancedIndirect              uintptr
	DrawInstancedIndirect                     uintptr
	Dispatch                                  uintptr
	DispatchIndirect                          uintptr
	RSSetState                                uintptr
	RSSetViewports                            uintptr
	RSSetScissorRects                         uintptr
	CopySubresourceRegion                     uintptr
	CopyResource                              uintptr
	UpdateSubresource                         uintptr
	CopyStructureCount                        uintptr
	ClearRenderTargetView                     uintptr
	ClearUnorderedAccessViewUint              uintptr
	ClearUnorderedAccessViewFloat             uintptr
	ClearDepthStencilView                     uintptr
	GenerateMips                              uintptr
	SetResourceMinLOD                         uintptr
	GetResourceMinLOD                         uintptr
	ResolveSubresource                        uintptr
	ExecuteCommandList                        uintptr
	HSSetShaderResources                      uintptr
	HSSetShader                               uintptr
	HSSetSamplers                             uintptr
	HSSetConstantBuffers                      uintptr
	DSSetShaderResources                      uintptr
	DSSetShader                               uintptr
	DSSetSamplers                             uintptr
	DSSetConstantBuffers                      uintptr
	CSSetShaderResources                      uintptr
	CSSetUnorderedAccessViews                 uintptr
	CSSetShader                               uintptr
	CSSetSamplers                             uintptr
	CSSetConstantBuffers                      uintptr
	VSGetConstantBuffers                      uintptr
	PSGetShaderResources                      uintptr
	PSGetShader                               uintptr
	PSGetSamplers                             uintptr
	VSGetShader                               uintptr
	PSGetConstantBuffers                      uintptr
	IAGetInputLayout                          uintptr
	IAGetVertexBuffers                        uintptr
	IAGetIndexBuffer                          uintptr
	GSGetConstantBuffers                      uintptr
	GSGetShader                               uintptr
	IAGetPrimitiveTopology                    uintptr
	VSGetShaderResources                      uintptr
	VSGetSamplers                             uintptr
	GetPredication                            uintptr
	GSGetShaderResources                      uintptr
	GSGetSamplers                             uintptr
	OMGetRenderTargets                        uintptr
	OMGetRenderTargetsAndUnorderedAccessViews uintptr
	OMGetBlendState                           uintptr
	OMGetDepthStencilState                    uintptr
	SOGetTargets                              uintptr
	RSGetState                                uintptr
	RSGetViewports                            uintptr
	RSGetScissorRects                         uintptr
	HSGetShaderResources                      uintptr
	HSGetShader                               uintptr
	HSGetSamplers                             uintptr
	HSGetConstantBuffers                      uintptr
	DSGetShaderResources                      uintptr
	DSGetShader                               uintptr
	DSGetSamplers                             uintptr
	DSGetConstantBuffers                      uintptr
	CSGetShaderResources                      uintptr
	CSGetUnorderedAccessViews                 uintptr
	CSGetShader                               uintptr
	CSGetSamplers                             uintptr
	CSGetConstantBuffers                      uintptr
	ClearState                                uintptr
	Flush                                     uintptr
	GetType                                   uintptr
	GetContextFlags                           uintptr
	FinishCommandList                         uintptr
}

func (c *ID3D11DeviceContext) Release() uint32 {
	ret, _, _ := syscall.SyscallN(c.lpVtbl.Release, uintptr(unsafe.Pointer(c)))
	return uint32(ret)
}

func (c *ID3D11DeviceContext) CopyResource(dst, src *ID3D11Texture2D) {
	syscall.SyscallN(c.lpVtbl.CopyResource, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(src)))
}

func (c *ID3D11DeviceContext) Map(resource *ID3D11Texture2D, subresource uint32, mapType uint32, mapFlags uint32, mapped *D3d11MappedSubresource) HRESULT {
	ret, _, _ := syscall.SyscallN(c.lpVtbl.Map, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(resource)), uintptr(subresource), uintptr(mapType), uintptr(mapFlags), uintptr(unsafe.Pointer(mapped)))
	return HRESULT(ret)
}

func (c *ID3D11DeviceContext) Unmap(resource *ID3D11Texture2D, subresource uint32) {
	syscall.SyscallN(c.lpVtbl.Unmap, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(resource)), uintptr(subresource))
}

func (c *ID3D11DeviceContext) Flush() {
	syscall.SyscallN(c.lpVtbl.Flush, uintptr(unsafe.Pointer(c)))
}

type ID3D11Texture2D struct{ lpVtbl *ID3D11Texture2DVtbl }

type ID3D11Texture2DVtbl struct {
	QueryInterface          uintptr
	AddRef                  uintptr
	Release                 uintptr
	GetDevice               uintptr
	GetPrivateData          uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	GetType                 uintptr
	SetEvictionPriority     uintptr
	GetEvictionPriority     uintptr
	GetDesc                 uintptr
}

func (t *ID3D11Texture2D) Release() uint32 {
	ret, _, _ := syscall.SyscallN(t.lpVtbl.Release, uintptr(unsafe.Pointer(t)))
	return uint32(ret)
}

func (t *ID3D11Texture2D) GetDesc(desc *D3d11Texture2dDesc) {
	syscall.SyscallN(t.lpVtbl.GetDesc, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(desc)))
}

type RECT struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type DxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates RECT
	AttachedToDesktop  uint32
	Rotation           uint32
	Monitor            uintptr
}

type DxgiOutduplDesc struct {
	ModeDesc                   DxgiModeDesc
	Rotation                   uint32
	DesktopImageInSystemMemory uint32
}

type DxgiModeDesc struct {
	Width            uint32
	Height           uint32
	RefreshRate      DxgiRational
	Format           uint32
	ScanlineOrdering uint32
	Scaling          uint32
}

type DxgiRational struct {
	Numerator   uint32
	Denominator uint32
}

type DxgiOutduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            uint32
	ProtectedContentMaskedOut uint32
	PointerPosition           DxgiOutduplPointerPosition
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

type DxgiOutduplPointerPosition struct {
	Position DxgiPoint
	Visible  uint32
}

type DxgiPoint struct {
	X int32
	Y int32
}

type D3d11Texture2dDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleDesc     DxgiSampleDesc
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type DxgiSampleDesc struct {
	Count   uint32
	Quality uint32
}

type D3d11SubresourceData struct {
	SysMem           uintptr
	SysMemPitch      uint32
	SysMemSlicePitch uint32
}

type D3d11MappedSubresource struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

func newDXGICapturer(opts *Options) (Capturer, error) {
	c := &dxgiCapturer{timeout: opts.Timeout}

	hr := coInitializeEx()
	if failed(hr) && hr != 0x80010106 { // RPC_E_CHANGED_MODE
		return nil, windowsError("CoInitializeEx", hr)
	}

	if err := c.initFactory(opts.AdapterIndex, opts.OutputIndex); err != nil {
		c.Close()
		return nil, err
	}

	return c, nil
}

func coInitializeEx() HRESULT {
	ret, _, _ := syscall.SyscallN(procCoInitializeEx.Addr(), 0, coinitMultithreaded)
	return HRESULT(ret)
}

func (c *dxgiCapturer) initFactory(adapterIndex, outputIndex int) error {
	var factory *IDXGIFactory1
	hr := createDXGIFactory1(&iidIDXGIFactory1, unsafe.Pointer(&factory))
	if failed(hr) {
		return windowsError("CreateDXGIFactory1", hr)
	}
	c.factory = factory

	var adapter *IDXGIAdapter1
	hr = factory.EnumAdapters1(uint32(adapterIndex), &adapter)
	if failed(hr) {
		return windowsError("EnumAdapters1", hr)
	}
	c.adapter = adapter
	var output *IDXGIOutput
	hr = adapter.EnumOutputs(uint32(outputIndex), &output)
	if failed(hr) {
		return windowsError("EnumOutputs", hr)
	}
	c.output = output

	var output1 *IDXGIOutput1
	hr = output.QueryInterface(&iidIDXGIOutput1, unsafe.Pointer(&output1))
	if failed(hr) {
		return windowsError("QueryInterface IDXGIOutput1", hr)
	}
	c.output1 = output1

	var desc DxgiOutputDesc
	hr = output.GetDesc(&desc)
	if failed(hr) {
		return windowsError("IDXGIOutput.GetDesc", hr)
	}
	width := int(desc.DesktopCoordinates.Right - desc.DesktopCoordinates.Left)
	height := int(desc.DesktopCoordinates.Bottom - desc.DesktopCoordinates.Top)
	if width <= 0 || height <= 0 {
		return errors.New("invalid desktop coordinates")
	}
	c.width = width
	c.height = height

	return c.initDeviceAndDuplication()
}

func (c *dxgiCapturer) initDeviceAndDuplication() error {
	var device *ID3D11Device
	var context *ID3D11DeviceContext
	var featureLevel uint32

	hr := d3d11CreateDevice(unsafe.Pointer(c.adapter), d3dDriverTypeUnknown, 0, d3d11CreateDeviceBgraSupport, nil, 0, d3d11SdkVersion, &device, &featureLevel, &context)
	if failed(hr) {
		return windowsError("D3D11CreateDevice", hr)
	}
	c.device = device
	c.context = context

	var dup *IDXGIOutputDuplication
	hr = c.output1.DuplicateOutput(device, &dup)
	if failed(hr) {
		return windowsError("IDXGIOutput1.DuplicateOutput", hr)
	}
	c.duplication = dup

	var dupDesc DxgiOutduplDesc
	hr = dup.GetDesc(&dupDesc)
	if !failed(hr) {
		c.width = int(dupDesc.ModeDesc.Width)
		c.height = int(dupDesc.ModeDesc.Height)
	}

	return nil
}

func createDXGIFactory1(riid *windowsGUID, factory unsafe.Pointer) HRESULT {
	ret, _, _ := syscall.SyscallN(procCreateDXGIFactory1.Addr(), uintptr(unsafe.Pointer(riid)), uintptr(factory))
	return HRESULT(ret)
}

func d3d11CreateDevice(adapter unsafe.Pointer, driverType uint32, software uintptr, flags uint32, featureLevels *uint32, featureLevelsCount uint32, sdkVersion uint32, device **ID3D11Device, featureLevel *uint32, context **ID3D11DeviceContext) HRESULT {
	ret, _, _ := syscall.SyscallN(procD3D11CreateDevice.Addr(), uintptr(adapter), uintptr(driverType), software, uintptr(flags), uintptr(unsafe.Pointer(featureLevels)), uintptr(featureLevelsCount), uintptr(sdkVersion), uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(featureLevel)), uintptr(unsafe.Pointer(context)))
	return HRESULT(ret)
}

func (c *dxgiCapturer) Capture() (*image.RGBA, error) {
	img, _, err := c.CaptureWithInfo()
	return img, err
}

func (c *dxgiCapturer) CaptureWithInfo() (*image.RGBA, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.duplication == nil {
		return nil, false, errors.New("dxgi duplication not initialized")
	}

	// Fast path: if we already have a frame, do a non-blocking poll.
	if c.lastImage != nil {
		var info DxgiOutduplFrameInfo
		var resource *IDXGIResource
		hr := c.duplication.AcquireNextFrame(0, &info, &resource)
		if hr == dxgiErrorWaitTimeout {
			return c.lastImage, true, nil
		}
		if hr == dxgiErrorAccessLost {
			return nil, false, errAccessLost
		}
		if failed(hr) {
			return nil, false, windowsError("AcquireNextFrame", hr)
		}
		// If there's no new frame, return cached image.
		if info.LastPresentTime == 0 && info.AccumulatedFrames == 0 {
			if resource != nil {
				resource.Release()
			}
			c.duplication.ReleaseFrame()
			return c.lastImage, true, nil
		}
		img, err := c.readFrame(resource)
		if err != nil {
			return nil, false, err
		}
		c.lastImage = img
		return img, false, nil
	}

	deadline := time.Now().Add(c.timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if c.lastImage != nil {
				return c.lastImage, true, nil
			}
			return nil, false, ErrTimeout
		}
		timeoutMS := uint32(remaining.Milliseconds())
		if timeoutMS == 0 {
			timeoutMS = 1
		}

		var info DxgiOutduplFrameInfo
		var resource *IDXGIResource
		hr := c.duplication.AcquireNextFrame(timeoutMS, &info, &resource)
		if hr == dxgiErrorWaitTimeout {
			if c.lastImage != nil {
				return c.lastImage, true, nil
			}
			return nil, false, ErrTimeout
		}
		if hr == dxgiErrorAccessLost {
			return nil, false, errAccessLost
		}
		if failed(hr) {
			return nil, false, windowsError("AcquireNextFrame", hr)
		}
		// If there's no desktop present yet, try again within the timeout.
		if info.LastPresentTime == 0 && info.AccumulatedFrames == 0 {
			if resource != nil {
				resource.Release()
			}
			c.duplication.ReleaseFrame()
			if c.lastImage != nil {
				return c.lastImage, true, nil
			}
			continue
		}

		img, err := c.readFrame(resource)
		if err != nil {
			return nil, false, err
		}
		c.lastImage = img
		return img, false, nil
	}
}

func (c *dxgiCapturer) readFrame(resource *IDXGIResource) (*image.RGBA, error) {
	defer func() {
		if resource != nil {
			resource.Release()
		}
		c.duplication.ReleaseFrame()
	}()

	var tex *ID3D11Texture2D
	hr := resource.QueryInterface(&iidID3D11Texture2D, unsafe.Pointer(&tex))
	if failed(hr) {
		return nil, windowsError("QueryInterface ID3D11Texture2D", hr)
	}
	defer tex.Release()

	var texDesc D3d11Texture2dDesc
	tex.GetDesc(&texDesc)

	if err := c.ensureStaging(&texDesc); err != nil {
		return nil, err
	}

	c.context.CopyResource(c.staging, tex)
	c.context.Flush()

	var mapped D3d11MappedSubresource
	hr = c.context.Map(c.staging, 0, 1, 0, &mapped) // D3D11_MAP_READ = 1
	if failed(hr) {
		return nil, windowsError("Map", hr)
	}
	defer c.context.Unmap(c.staging, 0)

	width := int(texDesc.Width)
	height := int(texDesc.Height)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	dstStride := img.Stride
	srcStride := int(mapped.RowPitch)
	srcPtr := mapped.Data

	switch texDesc.Format {
	case dxgiFormatB8G8R8A8Unorm, dxgiFormatB8G8R8A8UnormSRGB:
		for y := 0; y < height; y++ {
			src := unsafe.Slice((*byte)(unsafe.Pointer(srcPtr)), srcStride)
			dst := img.Pix[y*dstStride : y*dstStride+width*4]
			for x := 0; x < width; x++ {
				i := x * 4
				// BGRA -> RGBA
				dst[i+0] = src[i+2]
				dst[i+1] = src[i+1]
				dst[i+2] = src[i+0]
				// Desktop duplication often reports zero alpha; force opaque.
				dst[i+3] = 0xFF
			}
			srcPtr += uintptr(srcStride)
		}
	case dxgiFormatR8G8B8A8Unorm, dxgiFormatR8G8B8A8UnormSRGB:
		for y := 0; y < height; y++ {
			src := unsafe.Slice((*byte)(unsafe.Pointer(srcPtr)), srcStride)
			dst := img.Pix[y*dstStride : y*dstStride+width*4]
			for x := 0; x < width; x++ {
				i := x * 4
				// RGBA -> RGBA
				dst[i+0] = src[i+0]
				dst[i+1] = src[i+1]
				dst[i+2] = src[i+2]
				dst[i+3] = 0xFF
			}
			srcPtr += uintptr(srcStride)
		}
	case dxgiFormatR10G10B10A2Unorm:
		for y := 0; y < height; y++ {
			src := unsafe.Slice((*uint32)(unsafe.Pointer(srcPtr)), srcStride/4)
			dst := img.Pix[y*dstStride : y*dstStride+width*4]
			for x := 0; x < width; x++ {
				v := src[x]
				r10 := v & 0x3FF
				g10 := (v >> 10) & 0x3FF
				b10 := (v >> 20) & 0x3FF
				r8 := uint8((r10*255 + 511) / 1023)
				g8 := uint8((g10*255 + 511) / 1023)
				b8 := uint8((b10*255 + 511) / 1023)
				i := x * 4
				dst[i+0] = r8
				dst[i+1] = g8
				dst[i+2] = b8
				dst[i+3] = 0xFF
			}
			srcPtr += uintptr(srcStride)
		}
	case dxgiFormatR16G16B16A16Float:
		for y := 0; y < height; y++ {
			src := unsafe.Slice((*uint16)(unsafe.Pointer(srcPtr)), srcStride/2)
			dst := img.Pix[y*dstStride : y*dstStride+width*4]
			for x := 0; x < width; x++ {
				j := x * 4
				r := halfToFloat(src[j+0])
				g := halfToFloat(src[j+1])
				b := halfToFloat(src[j+2])
				// clamp and convert to 8-bit
				dst[j+0] = floatToByte(r)
				dst[j+1] = floatToByte(g)
				dst[j+2] = floatToByte(b)
				dst[j+3] = 0xFF
			}
			srcPtr += uintptr(srcStride)
		}
	default:
		return nil, errors.New("unsupported DXGI format: 0x" + convertutil.FormatHex(texDesc.Format))
	}

	return img, nil
}

func (c *dxgiCapturer) ensureStaging(desc *D3d11Texture2dDesc) error {
	if c.staging != nil && c.stagingFormat == desc.Format && c.stagingWidth == int(desc.Width) && c.stagingHeight == int(desc.Height) {
		return nil
	}
	if c.staging != nil {
		c.staging.Release()
		c.staging = nil
	}
	c.stagingFormat = 0
	c.stagingWidth = 0
	c.stagingHeight = 0

	d := *desc
	d.Usage = d3d11UsageStaging
	d.BindFlags = d3d11BindFlagNone
	d.CPUAccessFlags = d3d11CpuAccessRead
	d.MiscFlags = d3d11MiscFlagNone

	var staging *ID3D11Texture2D
	hr := c.device.CreateTexture2D(&d, nil, &staging)
	if failed(hr) {
		return windowsError("CreateTexture2D (staging)", hr)
	}
	c.staging = staging
	c.stagingFormat = d.Format
	c.stagingWidth = int(d.Width)
	c.stagingHeight = int(d.Height)

	return nil
}

func (c *dxgiCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.staging != nil {
		c.staging.Release()
		c.staging = nil
	}
	if c.duplication != nil {
		c.duplication.Release()
		c.duplication = nil
	}
	if c.output1 != nil {
		c.output1.Release()
		c.output1 = nil
	}
	if c.output != nil {
		c.output.Release()
		c.output = nil
	}
	if c.adapter != nil {
		c.adapter.Release()
		c.adapter = nil
	}
	if c.context != nil {
		c.context.Release()
		c.context = nil
	}
	if c.device != nil {
		c.device.Release()
		c.device = nil
	}
	if c.factory != nil {
		c.factory.Release()
		c.factory = nil
	}
	return nil
}

func windowsError(op string, hr HRESULT) error {
	return errors.New(op + " failed: HRESULT=0x" + convertutil.FormatHex(uint32(hr)))
}

func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) & 0x1
	exp := uint32(h>>10) & 0x1F
	frac := uint32(h & 0x3FF)

	if exp == 0 {
		if frac == 0 {
			return math.Float32frombits(sign << 31)
		}
		// subnormal
		for (frac & 0x400) == 0 {
			frac <<= 1
			exp--
		}
		exp++
		frac &= 0x3FF
	} else if exp == 31 {
		if frac == 0 {
			return math.Float32frombits((sign << 31) | 0x7F800000)
		}
		return math.Float32frombits((sign << 31) | 0x7F800000 | (frac << 13))
	}

	exp = exp + (127 - 15)
	bits := (sign << 31) | (exp << 23) | (frac << 13)
	return math.Float32frombits(bits)
}

func floatToByte(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}
