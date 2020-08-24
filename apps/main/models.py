from django.db import models


class Iban(models.Model):
    iban = models.CharField(max_length=22, unique=True)

    def __str__(self):
        return self.iban


class Bic(models.Model):
    bic = models.CharField(max_length=11, unique=True)

    def __str__(self):
        return self.bic


class BookingType(models.Model):
    name = models.CharField(max_length=50, unique=True)

    def __str__(self):
        return self.name


class Currency(models.Model):
    currency_short = models.CharField(max_length=3, unique=True)
    currency_long = models.CharField(max_length=100)

    def __str__(self):
        return self.currency_short


class Optionee(models.Model):
    name = models.CharField(max_length=200)
    iban = models.ForeignKey(Iban, on_delete=models.CASCADE)
    bic = models.ForeignKey(Bic, on_delete=models.CASCADE)

    def __str__(self):
        return self.name


class Transaction(models.Model):
    account = models.ForeignKey(Iban, on_delete=models.CASCADE)
    date_booking = models.DateField()
    date_imported = models.DateField(auto_now_add=True)
    booking_type = models.ForeignKey(BookingType, on_delete=models.CASCADE)
    optionee = models.ForeignKey(Optionee, on_delete=models.CASCADE)
    figure = models.DecimalField(max_digits=18, decimal_places=2)
    currency = models.ForeignKey(Currency, on_delete=models.CASCADE)
    reference = models.CharField(max_length=500, blank=True, default='')

    def __str__(self):
        return str(self.figure)
