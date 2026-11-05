Gem::Specification.new do |spec|
  spec.name = "x"
  spec.add_runtime_dependency "nokogiri", "~> 1.18"
  spec.add_development_dependency "rspec", "~> 3.13" if ENV["CI"]
  spec.add_dependency %q<thor>.freeze, [">= 1.0"]
  spec.add_dependency "faraday", FARADAY_VERSION
  spec.add_dependency "rack", [">= 2.2", "< 4"]
  spec.required_ruby_version = Gem::Requirement.new(">= 3.1")
end
