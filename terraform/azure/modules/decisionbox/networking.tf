resource "azurerm_resource_group" "main" {
  count    = var.create_resource_group ? 1 : 0
  name     = var.resource_group_name
  location = var.location
  tags     = local.all_tags
}

resource "azurerm_virtual_network" "main" {
  count               = var.create_vnet ? 1 : 0
  name                = "${var.cluster_name}-vnet"
  resource_group_name = local.resource_group_name
  location            = var.location
  address_space       = [var.vnet_cidr]
  tags                = local.all_tags
}

resource "azurerm_subnet" "nodes" {
  count                = var.create_vnet ? 1 : 0
  name                 = "${var.cluster_name}-nodes"
  resource_group_name  = local.resource_group_name
  virtual_network_name = azurerm_virtual_network.main[0].name
  address_prefixes     = [var.node_subnet_cidr]
}

# NSG for the AKS subnet
resource "azurerm_network_security_group" "aks" {
  count               = var.create_vnet && var.enable_nsg ? 1 : 0
  name                = "${var.cluster_name}-nsg"
  resource_group_name = local.resource_group_name
  location            = var.location
  tags                = local.all_tags
}

resource "azurerm_network_security_rule" "deny_ssh_default" {
  count                       = var.create_vnet && var.enable_nsg && length(var.nsg_allowed_ssh_cidrs) == 0 ? 1 : 0
  name                        = "deny-ssh-all"
  priority                    = 4000
  direction                   = "Inbound"
  access                      = "Deny"
  protocol                    = "Tcp"
  source_port_range           = "*"
  destination_port_range      = "22"
  source_address_prefix       = "*"
  destination_address_prefix  = "*"
  resource_group_name         = local.resource_group_name
  network_security_group_name = azurerm_network_security_group.aks[0].name
}

resource "azurerm_network_security_rule" "allow_ssh" {
  count                       = var.create_vnet && var.enable_nsg && length(var.nsg_allowed_ssh_cidrs) > 0 ? 1 : 0
  name                        = "allow-ssh-restricted"
  priority                    = 1000
  direction                   = "Inbound"
  access                      = "Allow"
  protocol                    = "Tcp"
  source_port_range           = "*"
  destination_port_range      = "22"
  source_address_prefixes     = var.nsg_allowed_ssh_cidrs
  destination_address_prefix  = "*"
  resource_group_name         = local.resource_group_name
  network_security_group_name = azurerm_network_security_group.aks[0].name
}

# Allow Azure Load Balancer health probes
resource "azurerm_network_security_rule" "allow_lb_probes" {
  count                       = var.create_vnet && var.enable_nsg ? 1 : 0
  name                        = "allow-lb-probes"
  priority                    = 100
  direction                   = "Inbound"
  access                      = "Allow"
  protocol                    = "*"
  source_port_range           = "*"
  destination_port_range      = "*"
  source_address_prefix       = "AzureLoadBalancer"
  destination_address_prefix  = "*"
  resource_group_name         = local.resource_group_name
  network_security_group_name = azurerm_network_security_group.aks[0].name
}

# Allow HTTP/HTTPS ingress — unrestricted (when allowed_ip_ranges is empty)
resource "azurerm_network_security_rule" "allow_http" {
  count                       = var.create_vnet && var.enable_nsg && length(var.allowed_ip_ranges) == 0 ? 1 : 0
  name                        = "allow-http-https"
  priority                    = 200
  direction                   = "Inbound"
  access                      = "Allow"
  protocol                    = "Tcp"
  source_port_range           = "*"
  destination_port_ranges     = ["80", "443"]
  source_address_prefix       = "Internet"
  destination_address_prefix  = "*"
  resource_group_name         = local.resource_group_name
  network_security_group_name = azurerm_network_security_group.aks[0].name
}

# Allow HTTP/HTTPS ingress — restricted to specified CIDRs
resource "azurerm_network_security_rule" "allow_http_restricted" {
  count                       = var.create_vnet && var.enable_nsg && length(var.allowed_ip_ranges) > 0 ? 1 : 0
  name                        = "allow-http-https"
  priority                    = 200
  direction                   = "Inbound"
  access                      = "Allow"
  protocol                    = "Tcp"
  source_port_range           = "*"
  destination_port_ranges     = ["80", "443"]
  source_address_prefixes     = var.allowed_ip_ranges
  destination_address_prefix  = "*"
  resource_group_name         = local.resource_group_name
  network_security_group_name = azurerm_network_security_group.aks[0].name
}

resource "azurerm_subnet_network_security_group_association" "aks" {
  count                     = var.create_vnet && var.enable_nsg ? 1 : 0
  subnet_id                 = azurerm_subnet.nodes[0].id
  network_security_group_id = azurerm_network_security_group.aks[0].id
}

# NAT Gateway for outbound internet from private nodes
resource "azurerm_public_ip" "nat" {
  count               = var.create_vnet && var.enable_nat_gateway ? 1 : 0
  name                = "${var.cluster_name}-nat-ip"
  resource_group_name = local.resource_group_name
  location            = var.location
  allocation_method   = "Static"
  sku                 = "Standard"
  tags                = local.all_tags
}

resource "azurerm_nat_gateway" "main" {
  count               = var.create_vnet && var.enable_nat_gateway ? 1 : 0
  name                = "${var.cluster_name}-nat"
  resource_group_name = local.resource_group_name
  location            = var.location
  sku_name            = "Standard"
  tags                = local.all_tags
}

resource "azurerm_nat_gateway_public_ip_association" "main" {
  count                = var.create_vnet && var.enable_nat_gateway ? 1 : 0
  nat_gateway_id       = azurerm_nat_gateway.main[0].id
  public_ip_address_id = azurerm_public_ip.nat[0].id
}

resource "azurerm_subnet_nat_gateway_association" "nodes" {
  count          = var.create_vnet && var.enable_nat_gateway ? 1 : 0
  subnet_id      = azurerm_subnet.nodes[0].id
  nat_gateway_id = azurerm_nat_gateway.main[0].id
}

locals {
  resource_group_name = var.create_resource_group ? azurerm_resource_group.main[0].name : var.resource_group_name
  resource_group_id   = var.create_resource_group ? azurerm_resource_group.main[0].id : "/subscriptions/${var.subscription_id}/resourceGroups/${var.resource_group_name}"
  vnet_id             = var.create_vnet ? azurerm_virtual_network.main[0].id : var.existing_vnet_id
  subnet_id           = var.create_vnet ? azurerm_subnet.nodes[0].id : var.existing_subnet_id

  all_tags = merge({
    managed_by = "terraform"
    project    = "decisionbox"
  }, var.tags)
}
