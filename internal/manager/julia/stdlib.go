package julia

// stdlibs are the UUIDs of the packages Julia ships (Pkg.Types.stdlibs() of Julia 1.12): their
// versions follow Julia's, so their compat entries are not updated. Former standard libraries
// that are registered packages now (Statistics, DelimitedFiles) are looked up like any other.
var stdlibs = map[string]bool{
	"0dad84c5-d112-42e6-8d28-ef12dabb789f": true, // ArgTools
	"56f22d72-fd6d-98f1-02f0-08ddc0907c33": true, // Artifacts
	"2a0f44e3-6c83-55bd-87e4-b1978d98bd5f": true, // Base64
	"8bf52ea8-c179-5cab-976a-9e18b702a9bc": true, // CRC32c
	"e66e0078-7015-5450-92f7-15fbd957f2ae": true, // CompilerSupportLibraries_jll
	"ade2ca70-3891-5945-98fb-dc099432e06a": true, // Dates
	"8ba89e20-285c-5b6f-9357-94700520ee1b": true, // Distributed
	"f43a241f-c20a-4ad4-852c-f6b1247861c6": true, // Downloads
	"7b1f6079-737a-58dc-b8bc-7a2ca5c1b5ee": true, // FileWatching
	"9fa8497b-333b-5362-9e8d-4d0656e87820": true, // Future
	"781609d7-10c4-51f6-84f2-b8444358ff6d": true, // GMP_jll
	"b77e0a4c-d291-57a0-90e8-8db25a27a240": true, // InteractiveUtils
	"ac6e5ff7-fb65-4e79-a425-ec3bc9c03011": true, // JuliaSyntaxHighlighting
	"d55e3150-da41-5e91-b323-ecfd1eec6109": true, // LLD_jll
	"47c5dbc3-30ba-59ef-96a6-123e260183d9": true, // LLVMLibUnwind_jll
	"4af54fe1-eca0-43a8-85a7-787d91b784e3": true, // LazyArtifacts
	"b27032c2-a3e7-50c8-80cd-2d36dbcbfd21": true, // LibCURL
	"deac9b47-8bc7-5906-a0fe-35ac56dc84c0": true, // LibCURL_jll
	"76f85450-5226-5b5a-8eaa-529ad045b433": true, // LibGit2
	"e37daf67-58a4-590a-8e99-b0245dd2ffc5": true, // LibGit2_jll
	"29816b5a-b9ab-546f-933c-edad1886dfa8": true, // LibSSH2_jll
	"183b4373-6708-53ba-ad28-60e28bb38547": true, // LibUV_jll
	"745a5e78-f969-53e9-954f-d19f2f74f4e3": true, // LibUnwind_jll
	"8f399da3-3557-5675-b5ff-fb832c97cbdb": true, // Libdl
	"37e2e46d-f89d-539d-b4ee-838fcccc9c8e": true, // LinearAlgebra
	"56ddb016-857b-54e1-b83d-db4d58db5568": true, // Logging
	"3a97d323-0669-5f0c-9066-3539efd106a3": true, // MPFR_jll
	"d6f4376e-aef5-505a-96c1-9c027394607a": true, // Markdown
	"a63ad114-7e13-5084-954f-fe012c677804": true, // Mmap
	"14a3606d-f60d-562e-9121-12d972cd8159": true, // MozillaCACerts_jll
	"ca575930-c2e3-43a9-ace4-1e988b2c1908": true, // NetworkOptions
	"4536629a-c528-5b80-bd46-f80d51c5b363": true, // OpenBLAS_jll
	"05823500-19ac-5b8b-9628-191a04bc5112": true, // OpenLibm_jll
	"458c3c95-2e84-50aa-8efc-19380b2a3a95": true, // OpenSSL_jll
	"efcefdf7-47ab-520b-bdef-62a2eaa19f15": true, // PCRE2_jll
	"44cfe95a-1eb2-52ea-b672-e2afdf69b78f": true, // Pkg
	"de0858da-6303-5e67-8744-51eddeeeb8d7": true, // Printf
	"9abbd945-dff8-562f-b5e8-e1ebf5ef1b79": true, // Profile
	"3fa0cd96-eef1-5676-8a61-b3b8758bbffb": true, // REPL
	"9a3f8284-a2c9-5f02-9a11-845980a1fd5c": true, // Random
	"ea8e919c-243c-51af-8825-aaa63cd721ce": true, // SHA
	"9e88b42a-f829-5b0c-bbe9-9e923198166b": true, // Serialization
	"1a1011a3-84de-559e-8e89-a11a2f7dc383": true, // SharedArrays
	"6462fe0b-24de-5631-8697-dd941f90decc": true, // Sockets
	"2f01184e-e22b-5df5-ae63-d93ebab69eaf": true, // SparseArrays
	"f489334b-da3d-4c2e-b8f0-e476e12c162b": true, // StyledStrings
	"4607b0f0-06f3-5cda-b6b1-a6196a1729e9": true, // SuiteSparse
	"bea87d4a-7f5b-5778-9afe-8cc45184846c": true, // SuiteSparse_jll
	"fa267f1f-6049-4f14-aa54-33bafae1ed76": true, // TOML
	"a4e569a6-e804-4fa4-b0f3-eef7a1d5b13e": true, // Tar
	"8dfed614-e22c-5e08-85e1-65c5234f0b40": true, // Test
	"cf7118a7-6976-5b1a-9a39-7adc72f591a4": true, // UUIDs
	"4ec0a83e-493e-50e2-b9ac-8f72acf5a8f5": true, // Unicode
	"83775a58-1f1d-513f-b197-d71354ab007a": true, // Zlib_jll
	"05ff407c-b0c1-5878-9df8-858cc2e60c36": true, // dSFMT_jll
	"8f36deef-c2a5-5394-99ed-8e07531fb29a": true, // libLLVM_jll
	"8e850b90-86db-534c-a0d3-1478176c7d93": true, // libblastrampoline_jll
	"8e850ede-7688-5339-a07c-302acd2aaf8d": true, // nghttp2_jll
	"3f19e933-33d8-53b3-aaab-bd5110c3b7a0": true, // p7zip_jll
}
