# ---------------------------------------------------------------------------
# Networking: private subnets across N AZs, egress via NAT, security groups
# implementing the plane-separation rules (§3 of ARCHITECTURE_VALIDATION.md):
# only the app SG may reach metadata stores; nothing reaches CAS except
# signed-channel fronting.
# ---------------------------------------------------------------------------

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "aegis" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
}

resource "aws_internet_gateway" "gw" {
  vpc_id = aws_vpc.aegis.id
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, var.az_count)
}

resource "aws_subnet" "private" {
  count             = length(local.azs)
  vpc_id            = aws_vpc.aegis.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 4, count.index)
}

resource "aws_subnet" "public" {
  count                    = length(local.azs)
  vpc_id                   = aws_vpc.aegis.id
  availability_zone        = local.azs[count.index]
  cidr_block               = cidrsubnet(var.vpc_cidr, 4, count.index + 8)
  map_public_ip_on_launch  = true
}

resource "aws_nat_gateway" "nat" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[0].id
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.aegis.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.gw.id
  }
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.aegis.id
  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.nat.id
  }
}

resource "aws_route_table_association" "private" {
  count          = length(local.azs)
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

resource "aws_route_table_association" "public" {
  count          = length(local.azs)
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# App plane SG (ingress engine / workers).
resource "aws_security_group" "app" {
  name_prefix = "aegis-app-"
  vpc_id      = aws_vpc.aegis.id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# Metadata-plane SG: TLS-only Postgres/Redis ingress from the app SG.
resource "aws_security_group" "metadata" {
  name_prefix = "aegis-metadata-"
  vpc_id      = aws_vpc.aegis.id

  ingress {
    description     = "PostgreSQL with forced TLS (rds.force_ssl=1)"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.app.id]
  }

  ingress {
    description     = "Redis with in-transit encryption"
    from_port       = 6379
    to_port         = 6379
    protocol        = "tcp"
    security_groups = [aws_security_group.app.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}
